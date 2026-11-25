package tui

import "charm.land/lipgloss/v2"

var (
	userBg     = lipgloss.Color("#333")
	userBorder = lipgloss.Color("#555")

	colorInfo    = lipgloss.Color("#4d97ff")
	colorWarning = lipgloss.Color("#ffd700")
	colorDanger  = lipgloss.Color("#e5484d") // destructive actions (delete)

	selectedPrefix        = "⯈ "
	multiUnselectedPrefix = "□ "
	multiSelectedPrefix   = "■ "
	checkPrefix           = "✓"
	xPrefix               = "🞪"

	// Fill for the currently-active option row in the system-ask form. Every
	// glyph on that row (prefix, marker, label) is painted with it so the
	// highlight reads as a solid bar rather than per-character gaps.
	askSelectedBG = lipgloss.Color("#1f252f")
	styleUser     = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#fff")).
			Background(lipgloss.Color("#27303b")).
			Padding(1, 2)
	styleThinking = lipgloss.NewStyle().Foreground(lipgloss.Color("#555")).Italic(true)
	styleResponse = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff"))
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("#b43300"))

	// Pending tools panel (between viewport and input)
	styleToolsHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("#999")).Bold(true)
	styleToolsBorder = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), true, false, false, false).
				BorderForeground(userBorder)
	stylePendingTool = lipgloss.NewStyle().Foreground(lipgloss.BrightCyan).Bold(true)
	styleToolArgs    = lipgloss.NewStyle().Foreground(lipgloss.Color("#888"))
	styleToolMeta    = lipgloss.NewStyle().Foreground(lipgloss.Color("#666"))

	// Completed tool call blocks (inline in history), dim per outcome, with a
	// one-column left margin so they sit slightly offset from the side.
	styleToolSuccess      = lipgloss.NewStyle().Foreground(lipgloss.Color("#3a72a6")).MarginLeft(1) // dim cyan
	styleToolFailure      = lipgloss.NewStyle().Foreground(lipgloss.Color("#9b2e26")).MarginLeft(1) // dim yellow
	styleToolSuccessTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("#4dd0e1")).Bold(true).MarginLeft(1)
	styleToolFailureTitle = lipgloss.NewStyle().Foreground(lipgloss.Color("#c9281d")).Bold(true).MarginLeft(1)

	styleStatusActive   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#005f00"))
	styleStatusThinking = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#5f5700"))
	styleStatusWaiting  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#005f9a"))
	// posting/ingest: the request is in flight, awaiting model output.
	styleStatusPosting   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#3d2b6b"))
	styleStatusParsing   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#4e6874"))
	styleStatusExecuting = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#3a72a6"))
	styleStatusError     = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#6b1f1f"))
	styleStatusIdle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#777"))
	// styleLogLine is the /log view's per-line renderer: it carries no colors,
	// only the viewport-width word wrap that keeps unbounded log records on
	// screen (the raw buffer stays unwrapped on disk).
	styleLogLine = lipgloss.NewStyle()

	// Subagent status bar entries reuse the main state colors where they map
	// (thinking/executing/parsing/waiting/error); these two cover the rest.
	styleCommand = styleUser.Foreground(lipgloss.BrightCyan)
	styleConsole = lipgloss.NewStyle().Foreground(lipgloss.Color("#aca236"))

	styleSubagentQueued = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#4a4a4a"))
	styleSubagentDone   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff")).Background(lipgloss.Color("#2d5a2d"))
	styleSubagentMeta   = lipgloss.NewStyle().Foreground(lipgloss.Color("#666"))
	borderNormal        = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), true, false).
				BorderForeground(userBorder).
				Padding(0, 1)
	stylePermissionPrompt = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder(), true).
				BorderForeground(lipgloss.Color("#FFD700")).
				Padding(1, 2)
	stylePermissionWarning = stylePermissionPrompt.BorderForeground(lipgloss.Color("#b43300"))

	// system-ask form (bottom panel swaps out the normal input while a form is
	// pending). One question renders at a time; text questions use the real
	// askTextInput element (its own styles handle text/placeholder/caret).
	styleAskHeader  = lipgloss.NewStyle().Foreground(colorInfo).Bold(true)
	styleAskHint    = lipgloss.NewStyle().Foreground(lipgloss.Color("#666"))
	styleAskPrompt  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff"))
	styleAskContext = lipgloss.NewStyle().Foreground(lipgloss.Color("#999")).Italic(true)
	styleAskOption  = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff"))
	styleAskChecked = lipgloss.NewStyle().Foreground(lipgloss.Color("#4dd0e1"))
	// Free-form filler text (empty "Type your own answer…") mirrors the input
	// placeholder gray from bubbles' DefaultStyles.
	styleAskPlaceholder    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleAskSelPlaceholder = styleAskPlaceholder.Background(askSelectedBG)
	// Active-row variants. styleAskSelected is the bare row fill (used for
	// unstyled prefixes and right-side padding); the _Option/_Checked forms add
	// the usual foregrounds on top of the same background.
	styleAskSelected   = lipgloss.NewStyle().Background(askSelectedBG)
	styleAskSelOption  = styleAskOption.Background(askSelectedBG)
	styleAskSelChecked = styleAskChecked.Background(askSelectedBG)
	styleAskYes        = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff"))
	styleAskNo         = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6439"))
	styleAskAnswer     = lipgloss.NewStyle().Foreground(lipgloss.Color("#fff"))
	styleAskBox        = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), true, false).
				BorderForeground(colorInfo).
				Padding(1, 2)

	colorHeaderForeground       = lipgloss.Color("#5ebbfd")
	colorHeaderBorderForeground = lipgloss.Color("#50748e")
	styleSystemHeaderText       = lipgloss.NewStyle().
					Bold(true).
					Foreground(colorHeaderForeground)
	styleSystemHeaderVersionText = lipgloss.NewStyle().
					Italic(true).
					Foreground(colorHeaderBorderForeground)
	styleSystemHeader = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(colorHeaderBorderForeground).
				Padding(1)

	styleDiffHeader = lipgloss.NewStyle().
			Foreground(colorHeaderForeground).
			MarginBottom(1).
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(colorHeaderBorderForeground)
)
