package agent

import (
	"bufio"
	"clankerv2/data"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// EnsureExecutor provisions an executor for a model under the given slot, joining
// an already-running instance when possible. Ownership is tracked as "legacy" and
// balanced with releaseExecutor(slot); new code should use EnsureExecutorForRun.
func EnsureExecutor(modelID string, modelType string, respChan chan<- any) (string, map[string]string, error) {
	url, headers, _, err := ensureExecutor(context.Background(), modelID, modelType, "legacy", false, false, respChan)
	if err != nil {
		return "", nil, err
	}
	return url, headers, nil
}

// ensureExecutor provisions an executor under a slot via refcounted lookup, in
// order: same model in the requested slot (reuse); any other slot running the
// same model (cross-slot join — how "inherit the calling agent's model" shares
// executors; isolated runs skip it so their teardown can't race another run);
// an in-flight launch of the same model (wait on it; single-flight, also skipped
// when isolated); otherwise evict the slot occupant and claim the launch. An
// evicted occupant still owned by live runs is orphaned — its slot entry goes
// away but the instance lives on until its last owner's release reaps it; at
// zero owners it is stopped. Persistent executors stay mapped and running warm
// after their last release so the next prompt skips a full model load. The long
// health wait runs outside the lock so other runs keep provisioning in parallel,
// and ctx abort tears down anything the launch started.
func ensureExecutor(ctx context.Context, modelID string, slot string, owner string, isolated bool, persistent bool, respChan chan<- any) (string, map[string]string, string, error) {
	cfg := GetAppConfig()
	if cfg == nil {
		return "", nil, "", fmt.Errorf("app config not loaded")
	}

	modelCfg := cfg.GetModelByID(modelID)
	if modelCfg == nil {
		return "", nil, "", fmt.Errorf("model %q not found in config", modelID)
	}

	var pending *pendingLaunch
	var evicted string // model ID of the occupant replaced this pass
	for {
		config.executorMu.Lock()
		if active, exists := config.ActiveExecutors[slot]; exists && active.ModelID == modelID {
			joinExecutor(active, owner)
			addAlias(active, slot)
			config.executorMu.Unlock()
			return resolveURL(modelCfg, active.Port), substituteHeaders(modelCfg.Headers), slot, nil
		}

		for otherSlot, active := range config.ActiveExecutors {
			if isolated {
				break // background runs own a private executor; never join another slot
			}
			if otherSlot != slot && active.ModelID == modelID {
				joinExecutor(active, owner)
				addAlias(active, slot)
				config.executorMu.Unlock()
				log.Printf("Executor join: run %s uses live executor for %q in slot %q", owner, modelID, otherSlot)
				return resolveURL(modelCfg, active.Port), substituteHeaders(modelCfg.Headers), otherSlot, nil
			}
		}

		// A boot of this model is already in flight: wait OUTSIDE the lock for it
		// to settle instead of starting a second server, then re-loop (a failed
		// boot publishes nothing, so we claim the launch ourselves). Isolated runs
		// never wait.
		if !isolated {
			if p, starting := config.StartingExecutors[modelID]; starting {
				config.executorMu.Unlock()
				log.Printf("Executor wait: run %s waiting for in-flight launch of %q", owner, modelID)
				select {
				case <-p.done:
					continue
				case <-ctx.Done():
					return "", nil, "", ctx.Err()
				}
			}
		}

		if active, exists := config.ActiveExecutors[slot]; exists {
			evicted = active.ModelID

			if len(active.Owners) > 0 && active.RefCount > 0 {
			// A live run still streams from this executor: orphan it instead of
			// killing it. Its slot entry goes away but the instance lives in
			// OrphanedExecutors until its last owner's release reaps it.
				log.Printf("Executor %q (model %s) still has %d live owner(s); orphaning from slot %q instead of killing", active.ModelID, active.ModelID, active.RefCount, slot)
				config.OrphanedExecutors[active] = true
		} else {
			// No owners left (a warm persistent chat server): stop it on switch.
			stopManagedExecutor(active)
			}
		removeAliases(active, slot)
		delete(config.ActiveExecutors, slot)
		}

		// Claim single-flight launch: while in StartingExecutors, other non-isolated
		// requests for modelID wait instead of booting a duplicate.
		pending = &pendingLaunch{modelID: modelID, done: make(chan struct{})}
		config.StartingExecutors[modelID] = pending
		if evicted != "" {
			sendConsole(respChan, fmt.Sprintf("Stopping active executor of type %q (model %q)", slot, evicted))
		}
		config.executorMu.Unlock()
		break
	}

	if !modelCfg.Managed {
		config.executorMu.Lock()
		config.ActiveExecutors[slot] = &ActiveExecutor{
			ModelID:    modelID,
			StartTime:  time.Now(),
			RefCount:   1,
			Owners:     map[string]bool{owner: true},
			Aliases:    map[string]bool{slot: true},
			Persistent: persistent,
		}
		config.executorMu.Unlock()
		settlePending(modelID, pending)
		return resolveURL(modelCfg, 0), substituteHeaders(modelCfg.Headers), slot, nil
	}

	url, headers, joinedSlot, err := launchManagedExecutorFn(ctx, modelID, slot, owner, persistent, respChan)
	settlePending(modelID, pending)
	if err != nil {
		return "", nil, "", err
	}
	return url, headers, joinedSlot, nil
}

// launchManagedExecutor runs the full managed boot for the run that claimed
// single-flight ownership of its model: port allocation, process/container
// start, publishing to ActiveExecutors, and the health wait. Any failure kills
// what was started and removes every pool entry, so no stale slot survives.
var launchManagedExecutorFn = launchManagedExecutor

func launchManagedExecutor(ctx context.Context, modelID string, slot, owner string, persistent bool, respChan chan<- any) (string, map[string]string, string, error) {
	cfg := GetAppConfig()
	if cfg == nil {
		return "", nil, "", fmt.Errorf("app config not loaded")
	}
	modelCfg := cfg.GetModelByID(modelID)

	msg := fmt.Sprintf("Starting new managed service for %s", modelID)
	log.Println(msg)
	sendConsole(respChan, msg)

	port, err := allocatePort(modelCfg.PortRange)
	if err != nil {
		return "", nil, "", fmt.Errorf("failed to allocate port: %w", err)
	}

	msg = fmt.Sprintf("Binding to port %d", port)
	log.Println(msg)
	sendConsole(respChan, msg)

	params := append([]string{}, modelCfg.Params...)
	if modelCfg.PortFlag != "" {
		// Containerized executors expose a fixed internal 8081; only the host port is dynamic.
		actualPort := strconv.Itoa(port)
		if modelCfg.Container != nil {
			actualPort = "8081"
		}
		params = append(params, modelCfg.PortFlag, actualPort)
	}

	var cmd *exec.Cmd
	containerName := ""
	var ring *outputRing // startup output capture (process boots only)
	var outPipe, errPipe io.ReadCloser

	if modelCfg.Container == nil {
		msg := fmt.Sprintf("Starting managed executor %q (type %q) on port %d: %s %v", modelID, slot, port, modelCfg.Command, params)
		log.Println(msg)
		sendConsole(respChan, msg)

		cmd = exec.Command(modelCfg.Command, params...)

		// Own process group so teardown kills the whole tree: managed commands are
		// often wrappers whose child is the real server, which would otherwise be
		// orphaned with its GPU RAM.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

		// Bounded startup-output capture: a pre-ready death is reported with this
		// tail. Pipes open before Start, reader goroutines after.
		ring = &outputRing{}
		var perr error
		outPipe, perr = cmd.StdoutPipe()
		if perr != nil {
			return "", nil, "", fmt.Errorf("failed to open executor stdout pipe: %w", perr)
		}
		errPipe, perr = cmd.StderrPipe()
		if perr != nil {
			return "", nil, "", fmt.Errorf("failed to open executor stderr pipe: %w", perr)
		}
	} else {
		containerName = fmt.Sprintf("clanker-%s", modelID)
		podmanArgs := []string{"run", "-d", "--name", containerName, "-p", fmt.Sprintf("%d:8081", port)}
		switch strings.ToLower(modelCfg.Container.GPUBackend) {
		case "cuda":
			podmanArgs = append(podmanArgs, "--device", "nvidia.com/gpu=all")
		case "vulkan":
			podmanArgs = append(podmanArgs, "--device", "/dev/dri")
		}

		podmanArgs = append(podmanArgs, modelCfg.Container.Args...)
		for hostPath, contPath := range modelCfg.Container.VolumeMappings {
			podmanArgs = append(podmanArgs, "-v", fmt.Sprintf("%s:%s:Z", hostPath, contPath))
		}

		podmanArgs = append(podmanArgs, modelCfg.Container.Image)
		// Omit modelCfg.Command: the image's ENTRYPOINT is the server; only params are passed.
		podmanArgs = append(podmanArgs, params...)

		msg := fmt.Sprintf("Starting containerized executor %q (type %q) on port %d: podman %v", modelID, slot, port, podmanArgs)
		log.Println(msg)
		sendConsole(respChan, msg)

		cmd = exec.Command("podman", podmanArgs...)
	}

	if err := cmd.Start(); err != nil {
		return "", nil, "", fmt.Errorf("failed to start executor: %w", err)
	}

	// Reap the process exactly once so its death is observable from the wait
	// loop. The reaper records the exit code and lets both capture goroutines
	// drain the pipes before closing exitCh, so a death report carries the full
	// output. Container boots have no long-lived process; liveness goes via podman
	// inspect instead (exitCh stays nil).
	var exitCh chan struct{}
	var exitCode func() int
	if modelCfg.Container == nil {
		outDone := make(chan struct{})
		errDone := make(chan struct{})
		go func() {
			for sc := bufio.NewScanner(outPipe); sc.Scan(); {
				ring.appendLine(sc.Text())
			}
			close(outDone)
		}()
		go func() {
			for sc := bufio.NewScanner(errPipe); sc.Scan(); {
				ring.appendLine("[stderr] " + sc.Text())
			}
			close(errDone)
		}()

		exitCh = make(chan struct{})
		code := 0
		go func() {
			if err := cmd.Wait(); err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					code = ee.ExitCode()
				} else {
					code = -1 // killed by a signal or other non-exit death
				}
			}
			// A child holding the pipes open would defer detection until timeout; model servers don't.
			<-outDone
			<-errDone
			close(exitCh)
		}()
		exitCode = func() int { return code }
	}

	// `podman run -d` returns once the container is created, so there's no
	// long-lived process to track; cleanup keys on ContainerName instead.
	active := &ActiveExecutor{
		ModelID:       modelID,
		ContainerName: containerName,
		Port:          port,
		StartTime:     time.Now(),
		RefCount:      1,
		Owners:        map[string]bool{owner: true},
		Aliases:       map[string]bool{slot: true},
		Persistent:    persistent,
	}
	if modelCfg.Container == nil {
		active.Process = cmd.Process
	}

	config.executorMu.Lock()
	config.ActiveExecutors[slot] = active
	config.executorMu.Unlock()

	msg = "Waiting for server to become ready (timeout 10m)..."
	log.Println(msg)
	sendConsole(respChan, msg)

	if containerName != "" {
		// Best-effort container boot-log stream until the wait ends; a logging
		// hiccup must never fail the boot.
		stopLogs := make(chan bool, 1)
		go func() {
			logCmd := exec.Command("podman", "logs", "-f", containerName)
			stdout, err := logCmd.StdoutPipe()
			if err != nil {
				log.Printf("Error creating logs pipe: %v", err)
				return
			}

			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					line := scanner.Text()
					sendConsole(respChan, "[Executor] "+line)
				}
			}()

			if err := logCmd.Start(); err != nil {
				log.Printf("Error starting logs command: %v", err)
				return
			}

			<-stopLogs
			_ = logCmd.Process.Kill()
		}()
		defer func() { stopLogs <- true }()
	}

	// Containers have no process to watch; passing waitProc=nil also keeps
	// waitForServerReady from misreading the podman CLI's normal exit as a boot failure.
	var waitProc *os.Process
	if modelCfg.Container == nil {
		waitProc = cmd.Process
	}

	// Every failure path must tear down what was started and clear the
	// ActiveExecutors entry, or a later retry could join a corpse.
	needsCleanup := true
	defer func() {
		if needsCleanup {
			cleanupExecutor(cmd.Process, containerName)
		}
	}()

	if err := waitForServerReady(ctx, modelCfg, port, waitProc, exitCh, exitCode, ring, respChan); err != nil {
		return "", nil, "", err // needsCleanup stays true: the defer kills + unmaps
	}

	msg = "Server is ready!"
	log.Println(msg)
	sendConsole(respChan, msg)
	needsCleanup = false
	return resolveURL(modelCfg, port), substituteHeaders(modelCfg.Headers), slot, nil
}

// joinExecutor records one more owner on a live executor (called with the pool
// lock held). It also aliases the executor into any slot that already points
// at it so map lookups from either side stay consistent.
func joinExecutor(active *ActiveExecutor, owner string) {
	if active.Owners == nil {
		active.Owners = make(map[string]bool)
	}
	active.RefCount++
	active.Owners[owner] = true
}

// addAlias records a slot as referencing an executor (called with the pool
// lock held).
func addAlias(active *ActiveExecutor, slot string) {
	if active.Aliases == nil {
		active.Aliases = make(map[string]bool)
	}
	active.Aliases[slot] = true
}

// removeAliases drops a slot's reference from an executor's alias set (called
// with the pool lock held).
func removeAliases(active *ActiveExecutor, slots ...string) {
	for _, s := range slots {
		delete(active.Aliases, s)
	}
}

// stopManagedExecutor tears down a managed executor's process tree or container;
// unmanaged models and missing handles are no-ops.
func stopManagedExecutor(active *ActiveExecutor) {
	if active.Process != nil {
		p := active.Process
		log.Printf("Stopping managed executor %q: killing process group %d", active.ModelID, p.Pid)
		// Kill the whole process group first (Setpgid at launch); fall back to a direct kill.
		if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
			if err2 := p.Kill(); err2 != nil {
				log.Printf("Error killing executor process for %s: %v (group kill: %v)", active.ModelID, err2, err)
			}
		}
	} else if active.ContainerName != "" {
		log.Printf("Stopping managed executor %q: removing container %s", active.ModelID, active.ContainerName)
		if err := exec.Command("podman", "rm", "-f", active.ContainerName).Run(); err != nil {
			log.Printf("Error removing executor container %s: %v", active.ContainerName, err)
		}
	} else {
		log.Printf("Releasing executor slot for %q: remote endpoint, nothing to stop", active.ModelID)
	}
}

// releaseExecutorOwnership returns one unit of ownership back to the pool. At
// zero owners the executor is removed from every slot that references it and,
// if managed, stopped — which frees GPU RAM — including orphaned instances.
// Persistent executors (the interactive chat model) are the exception: their
// last release keeps them mapped and running warm for the next prompt; they die
// only when a different model evicts them or the app shuts down.
func releaseExecutorOwnership(handle *executorAcquisition) {
	if handle == nil {
		return
	}

	config.executorMu.Lock()
	// Locate by OWNER, not slot: after a cross-slot join or mid-run model switch
	// the run's slot may point at a different model's executor now.
	var active *ActiveExecutor
	for _, v := range config.ActiveExecutors {
		if v.Owners[handle.Owner] {
			active = v
			break
		}
	}
	isOrphan := false
	if active == nil {
		// Evicted while still owned: the instance lives on in OrphanedExecutors.
		for v := range config.OrphanedExecutors {
			if v.Owners[handle.Owner] {
				active = v
				isOrphan = true
				break
			}
		}
	}
	if active == nil {
		config.executorMu.Unlock()
		log.Printf("Release of slot %q by owner %s: no live executor (already reaped?)", handle.Slot, handle.Owner)
		return
	}

	delete(active.Owners, handle.Owner)
	if active.RefCount > 0 {
		active.RefCount--
	}
	ownersLeft := len(active.Owners)
	config.executorMu.Unlock()

	if ownersLeft > 0 {
		log.Printf("Release of slot %q by owner %s: %d owner(s) remain, executor stays up", handle.Slot, handle.Owner, ownersLeft)
		return
	}

	// Last owner out. Persistent instances stay mapped and running at zero owners
	// — decided by the instance, not this handle, so a subagent that inherited
	// the warm chat model can't tear down the shared server; eviction or app
	// shutdown is what eventually kills them.
	config.executorMu.Lock()
	if isOrphan {
		delete(config.OrphanedExecutors, active)
	} else if !active.Persistent {
		for k := range active.Aliases {
			delete(config.ActiveExecutors, k)
		}
		// No alias bookkeeping (legacy path): fall back to identity matching.
		if len(active.Aliases) == 0 {
			for k, v := range config.ActiveExecutors {
				if v == active {
					delete(config.ActiveExecutors, k)
				}
			}
		}
	}
	config.executorMu.Unlock()

	if active.Persistent && !isOrphan {
		log.Printf("Executor for %q released by run %s: kept warm (persistent chat server, no owners)", active.ModelID, handle.Owner)
		return
	}

	stopManagedExecutor(active)
	log.Printf("Executor for %q fully released (no owners left)", active.ModelID)
}

// releaseExecutor is the legacy whole-slot teardown for background jobs predating
// per-run ownership: stops the slot's executor when no run still references it,
// leaving live-owner slots alone so a model switch never kills another run's
// server. Use releaseExecutorOwnership for ensureExecutor acquisitions.
func releaseExecutor(modelType string) {
	config.executorMu.Lock()
	active, exists := config.ActiveExecutors[modelType]
	if !exists {
		config.executorMu.Unlock()
		return
	}

	stillOwned := active.RefCount > 0 && len(active.Owners) > 0
	delete(config.ActiveExecutors, modelType)
	config.executorMu.Unlock()

	if stillOwned {
		log.Printf("Refusing to release executor %q: %d run(s) still reference it", active.ModelID, active.RefCount)
		return
	}

	stopManagedExecutor(active)
	log.Printf("Releasing executor slot %q (model %q)", modelType, active.ModelID)
}

// waitForServerReady polls until the server reports ready, failing fast when
// there's no point waiting: ctx cancellation (the run escaped and the caller
// tears down), the launched process dying (exitCh — a dead server can never
// heal), a container reaching a terminal state or vanishing (only podman
// inspect reveals that death), and a 10-minute timeout. Identical probe results
// are deduped before reporting.
func waitForServerReady(ctx context.Context, modelCfg *data.ModelConfig, port int, proc *os.Process, exitCh <-chan struct{}, exitCode func() int, ring *outputRing, respChan chan<- any) error {
	timeout := time.After(10 * time.Minute)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	probeURL := resolveURL(modelCfg, port)
	if modelCfg.Executor == "llama.cpp" {
		// llama-server answers 503 on API routes until the model finishes loading,
		// so a bare base-URL probe would mark boot successful too early; a 2xx
		// from /health means the model is actually usable.
		probeURL = probeURL + "/health"
	}

	lastReported := ""
	for {
		select {
		case <-ctx.Done():
			// The run escaped: return promptly so the caller's failure path tears down.
			log.Printf("Executor startup aborted: %v", ctx.Err())
			return fmt.Errorf("executor startup aborted: %w", ctx.Err())
		case <-timeout:
			return fmt.Errorf("timed out waiting for server to become ready after 10 minutes")
		case <-exitCh: // never fires when exitCh is nil (container boots)
			// A dead process can't heal itself: fail immediately with the last captured output.
			code := -1
			if exitCode != nil {
				code = exitCode()
			}
			var lines []string
			if ring != nil {
				lines = ring.tailLines()
			}
			return failBoot(fmt.Sprintf("executor process died during startup (exit code %d)", code), lines, respChan)
		case <-tick.C:
			// Container boots have no process; podman inspect is the only death signal.
			if proc == nil {
				if name := activeContainerName(port); name != "" {
					status, detail, ok := containerState(name)
					switch {
					case !ok:
						return failBoot(fmt.Sprintf("executor container %q disappeared during startup", name), podmanLogTail(name), respChan)
					case status == "exited" || status == "dead":
						reason := fmt.Sprintf("executor container %q failed to launch (state %s", name, status)
						if detail != "" {
							reason += ", " + detail
						}
						return failBoot(reason+")", podmanLogTail(name), respChan)
					}
				}
			}

			ready, detail := probeServer(probeURL)
			if ready {
				return nil
			}
			if detail != lastReported {
				msg := fmt.Sprintf("Waiting for server... (%s)", detail)
				log.Println(msg)
				sendConsole(respChan, msg)
				lastReported = detail
			}
		}
	}
}

// failBoot logs + reports a boot failure with its bounded captured output;
// teardown is the caller's job.
func failBoot(reason string, lines []string, respChan chan<- any) error {
	out := strings.TrimSpace(strings.Join(lines, "\n"))
	if out != "" {
		if len(out) > 2000 {
			out = "..." + out[len(out)-2000:]
		}
		reason += ": " + out
	}
	log.Println("[Executor] " + reason)
	sendConsole(respChan, "[Executor] "+reason)
	return errors.New(reason)
}

func activeContainerName(port int) string {
	config.executorMu.RLock()
	defer config.executorMu.RUnlock()
	for _, e := range config.ActiveExecutors {
		if e.Port == port && e.ContainerName != "" {
			return e.ContainerName
		}
	}
	return ""
}

// containerState asks podman for a container's lifecycle state. ok=false means
// it no longer exists; transient CLI errors report an empty status so polling continues.
func containerState(name string) (status, detail string, ok bool) {
	out, err := exec.Command("podman", "inspect", "--format", "{{.State.Status}} {{.State.ExitCode}}", name).CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(s), "no such container") {
			return "", s, false
		}
		return "", "", true
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) == 0 {
		return "", "unknown state", true
	}
	status = parts[0]
	if len(parts) > 1 && parts[1] != "0" {
		detail = "exit code " + parts[1]
	}
	return status, detail, true
}

// podmanLogTail fetches a container's last output lines for death reports.
func podmanLogTail(name string) []string {
	out, err := exec.Command("podman", "logs", "--tail", "20", name).CombinedOutput()
	if err != nil {
		return nil
	}
	var kept []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			kept = append(kept, l)
		}
	}
	return kept
}

// sendConsole delivers a best-effort, non-blocking console line so startup
// bookkeeping can't deadlock on a stalled UI consumer; every line is also logged.
func sendConsole(respChan chan<- any, msg string) {
	if respChan == nil {
		return
	}
	select {
	case respChan <- data.Response{Type: "console", Message: msg}:
	default:
	}
}

// outputRing is a bounded, goroutine-safe tail of startup output for death reports.
type outputRing struct {
	mu    sync.Mutex
	lines []string
}

func (r *outputRing) appendLine(s string) {
	if s == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, s)
	if len(r.lines) > 40 {
		r.lines = r.lines[len(r.lines)-40:]
	}
}

func (r *outputRing) tailLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// pendingLaunch marks an in-progress boot so concurrent runs for the same model
// wait instead of starting duplicates. A failed boot publishes nothing, so its
// waiters re-loop and claim a fresh launch themselves.
type pendingLaunch struct {
	modelID string
	done    chan struct{}
}

func settlePending(modelID string, p *pendingLaunch) {
	config.executorMu.Lock()
	// Only clear the entry if we still own it: an isolated run may have replaced it mid-boot.
	if cur, ok := config.StartingExecutors[modelID]; ok && cur == p {
		delete(config.StartingExecutors, modelID)
	}
	config.executorMu.Unlock()
	close(p.done)
}

// probeServer runs one readiness check: ready only on 2xx; other statuses are
// reported back as a reason string.
func probeServer(probeURL string) (ready bool, detail string) {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(probeURL)
	if err != nil {
		detail = fmt.Sprintf("no response yet (%s)", shortErr(err))
		return false, detail
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		log.Printf("Health probe %s: %d (ready)", probeURL, resp.StatusCode)
		return true, ""
	}

	detail = fmt.Sprintf("HTTP %d", resp.StatusCode)
	if s := strings.TrimSpace(string(body)); s != "" {
		if len(s) > 80 {
			s = s[:80] + "..."
		}
		detail += ": " + s
	}
	log.Printf("Health probe %s: not ready (%s)", probeURL, detail)
	return false, detail
}

func shortErr(err error) string {
	s := err.Error()
	if strings.Contains(s, "connection refused") {
		return "port not listening yet"
	}
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return s
}

// cleanupExecutor tears down a failed boot and clears its ActiveExecutors entry
// so the next ensureExecutor call retries cleanly instead of joining a corpse.
func cleanupExecutor(proc *os.Process, containerName string) {
	config.executorMu.Lock()
	var active *ActiveExecutor
	for k, v := range config.ActiveExecutors {
		if (containerName != "" && v.ContainerName == containerName) ||
			(proc != nil && v.Process == proc) {
			active = v
			delete(config.ActiveExecutors, k)
		}
	}
	config.executorMu.Unlock()

	if active != nil {
		stopManagedExecutor(active)
	}
}

func resolveURL(cfg *data.ModelConfig, port int) string {
	if cfg.URL != "" {
		return cfg.URL
	}
	return fmt.Sprintf("http://localhost:%d", port)
}

func allocatePort(portRange string) (int, error) {
	if portRange == "" {
		return 8080, nil // Default
	}

	parts := strings.Split(portRange, "-")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid port range format, expected 'min-max'")
	}

	min, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, err
	}
	max, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, err
	}

	for range 10 {
		port := rand.Intn(max-min+1) + min
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err == nil {
			ln.Close()
			return port, nil
		}
	}

	return 0, fmt.Errorf("could not find an available port in range %s", portRange)
}

func substituteHeaders(headers map[string]string) map[string]string {
	result := make(map[string]string)
	for k, v := range headers {
		substituted := v
		for _, envPair := range os.Environ() {
			pair := strings.SplitN(envPair, "=", 2)
			if len(pair) != 2 {
				continue
			}
			key := pair[0]
			val := pair[1]
			placeholder := fmt.Sprintf("${%s}", key)
			if strings.Contains(substituted, placeholder) {
				substituted = strings.ReplaceAll(substituted, placeholder, val)
			}
		}
		result[k] = substituted
	}
	return result
}
