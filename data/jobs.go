package data

import "time"

func InsertActiveJob(inputPath string) (int64, error) {
	result, err := db.Exec(
		"INSERT INTO active_jobs (inserted_at, input) VALUES (?, ?)",
		time.Now().UTC(), inputPath,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func ListActiveJobs() ([]Job, error) {
	rows, err := db.Query("SELECT id, inserted_at, input FROM active_jobs ORDER BY inserted_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.InsertedAt, &j.InputPath); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

func ListHistoricalJobs() ([]Job, error) {
	rows, err := db.Query("SELECT id, inserted_at, input FROM historical_jobs ORDER BY inserted_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.InsertedAt, &j.InputPath); err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}
