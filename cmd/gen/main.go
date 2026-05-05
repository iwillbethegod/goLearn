package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
)

func main() {
	dir := flag.String("dir", "data", "output directory")
	files := flag.Int("files", 4, "number of CSV files to write")
	rows := flag.Int("rows", 250, "rows per file")
	dupPct := flag.Int("dup", 15, "percent of rows that duplicate users from prior files")
	seed := flag.Int64("seed", 42, "rng seed")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	rng := rand.New(rand.NewSource(*seed))

	// pool of "known" emails to recycle as duplicates
	type known struct{ id, name, email string }
	var pool []known

	id := 0
	for i := 0; i < *files; i++ {
		path := filepath.Join(*dir, fmt.Sprintf("users_%c.csv", 'a'+i))
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		w := csv.NewWriter(f)
		_ = w.Write([]string{"id", "name", "email"})

		written := 0
		for r := 0; r < *rows; r++ {
			if i > 0 && len(pool) > 0 && rng.Intn(100) < *dupPct {
				k := pool[rng.Intn(len(pool))]
				_ = w.Write([]string{k.id, k.name, k.email})
			} else {
				id++
				k := known{
					id:    fmt.Sprintf("u%05d", id),
					name:  fmt.Sprintf("User %d", id),
					email: fmt.Sprintf("user%d@example.com", id),
				}
				_ = w.Write([]string{k.id, k.name, k.email})
				pool = append(pool, k)
			}
			written++
		}
		w.Flush()
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
		log.Printf("wrote %s (%d rows)", path, written)
	}
}
