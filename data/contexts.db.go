package data

import (
	"fmt"
	"time"
)

func LoadContexts() ([]Context, error) {
	rows, err := db.Query(`SELECT id, name, thinking, inserted_at, updated_at FROM contexts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contexts []Context
	for rows.Next() {
		var c Context
		var thinkingInt int
		if err := rows.Scan(&c.ID, &c.Name, &thinkingInt, &c.InsertedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		c.Thinking = thinkingInt != 0
		contexts = append(contexts, c)
	}
	return contexts, rows.Err()
}

func LoadContext(id int64) (*Context, error) {
	var c Context
	var thinkingInt int
	err := db.QueryRow(`SELECT id, name, thinking, inserted_at, updated_at FROM contexts WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &thinkingInt, &c.InsertedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	c.Thinking = thinkingInt != 0

	prompts, err := LoadContextPrompts(&c)
	if err != nil {
		return &c, err
	}

	c.Prompts = prompts
	return &c, nil
}

func SaveContext(ctx *Context) (*Context, error) {
	now := time.Now().UTC()
	if ctx.ID == 0 || ctx.InsertedAt.IsZero() {
		ctx.InsertedAt = now
	}
	ctx.UpdatedAt = now

	var id int64
	if ctx.ID > 0 {
		fmt.Println("updating")
		_, err := db.Exec(`
			UPDATE contexts SET name = ?, thinking = ?, inserted_at = ?, updated_at = ?
			WHERE id = ?
		`, ctx.Name, b2i(ctx.Thinking), ctx.InsertedAt, ctx.UpdatedAt, ctx.ID)
		if err != nil {
			return nil, err
		}
		// When updating, LastInsertId() is unreliable. Use the provided ID.
		id = ctx.ID
	} else {
		fmt.Println("inserting")
		// INSERT path: Omit 'id' so SQLite handles auto-incrementing
		results, err := db.Exec(`
  			INSERT INTO contexts (name, thinking, inserted_at, updated_at)
			VALUES (?, ?, ?, ?)
        `, ctx.Name, b2i(ctx.Thinking), ctx.InsertedAt, ctx.UpdatedAt)

		if err != nil {
			return nil, err
		}
		id, _ = results.LastInsertId()
	}

	// Terrible
	fmt.Println("Clearing previous prompts")
	RemoveContextPrompts(id)
	for i, prompt := range ctx.Prompts {
		fmt.Println("Upserting prompts")
		UpsertContextPrompt(id, prompt.ID, i)
	}

	return &Context{
		ID:         id,
		Name:       ctx.Name,
		Prompts:    ctx.Prompts,
		Thinking:   ctx.Thinking,
		InsertedAt: ctx.InsertedAt,
		UpdatedAt:  ctx.UpdatedAt,
	}, nil
}

func DeleteContext(ctx *Context) error {
	_, err := db.Exec(`DELETE FROM contexts WHERE id = ?`, ctx.ID)
	return err
}

func DeletePrompt(ctx *Prompt) error {
	_, err := db.Exec(`DELETE FROM prompts WHERE id = ?`, ctx.ID)
	return err
}

func SavePrompt(ctx *Prompt) (*Prompt, error) {
	now := time.Now().UTC()
	if ctx.ID == 0 || ctx.InsertedAt.IsZero() {
		ctx.InsertedAt = now
	}
	ctx.UpdatedAt = now

	var id int64
	if ctx.ID > 0 {
		// UPDATE path: Force an upsert using ON CONFLICT
		_, err := db.Exec(`
            INSERT INTO prompts (id, name, content, inserted_at, updated_at)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(id) DO UPDATE SET 
                name = excluded.name, 
                content = excluded.content, 
                updated_at = excluded.updated_at
        `, ctx.ID, ctx.Name, ctx.Content, ctx.InsertedAt, ctx.UpdatedAt)

		if err != nil {
			return nil, err
		}
		// When updating, LastInsertId() is unreliable. Use the provided ID.
		id = ctx.ID
	} else {
		// INSERT path: Omit 'id' so SQLite handles auto-incrementing
		results, err := db.Exec(`
            INSERT INTO prompts (name, content, inserted_at, updated_at)
            VALUES (?, ?, ?, ?)
        `, ctx.Name, ctx.Content, ctx.InsertedAt, ctx.UpdatedAt)

		if err != nil {
			return nil, err
		}
		id, _ = results.LastInsertId()
	}

	return &Prompt{
		ID:         id,
		Name:       ctx.Name,
		Content:    ctx.Content,
		InsertedAt: ctx.InsertedAt,
		UpdatedAt:  ctx.UpdatedAt,
	}, nil
}

func LoadPrompts() ([]Prompt, error) {
	rows, err := db.Query(`SELECT id, name, content, inserted_at, updated_at FROM prompts ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prompts []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.ID, &p.Name, &p.Content, &p.InsertedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		prompts = append(prompts, p)
	}
	return prompts, rows.Err()
}

func LoadPrompt(id int64) (*Prompt, error) {
	var c Prompt
	err := db.QueryRow(`SELECT id, name, content, inserted_at, updated_at FROM prompts WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Content, &c.InsertedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}

	return &c, err
}

func LoadContextPrompts(ctx *Context) ([]Prompt, error) {
	rows, err := db.Query(`
		SELECT p.id, p.name, p.content, p.inserted_at, p.updated_at 
		FROM context_prompts cp 
		JOIN prompts p ON cp.prompt_id = p.id 
		WHERE cp.context_id = ? 
		ORDER BY cp.prompt_order`, ctx.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var prompts = make([]Prompt, 0)
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.ID, &p.Name, &p.Content, &p.InsertedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		prompts = append(prompts, p)
	}
	return prompts, rows.Err()
}

func UpsertContextPrompt(ctxID int64, promptID int64, order int) error {
	_, err := db.Exec(`
		INSERT INTO context_prompts (context_id, prompt_id, prompt_order)
		VALUES (?, ?, ?)
		ON CONFLICT(context_id, prompt_id) DO UPDATE SET prompt_order=excluded.prompt_order
	`, ctxID, promptID, order)
	return err
}

func RemoveContextPrompt(ctx *Context, prompt *Prompt) error {
	_, err := db.Exec(`DELETE FROM context_prompts WHERE context_id = ? AND prompt_id = ?`, ctx.ID, prompt.ID)
	return err
}

func RemoveContextPrompts(ctxID int64) error {
	_, err := db.Exec(`DELETE FROM context_prompts WHERE context_id = ?`, ctxID)
	return err
}

// b2i converts bool to int (0/1) for SQLite compatibility
func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
