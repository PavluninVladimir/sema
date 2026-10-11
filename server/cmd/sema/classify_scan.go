package main

// Замер классификации на своих данных: прогнать нынешние и новые приметы
// по сохранённым промптам и посчитать, что изменилось бы. Считаем **на
// той же машине, где лежит база**: увозить личные тексты на другую машину
// ради замера незачем, а в stdout уходят только числа.
//
// База открывается только на чтение и без замка: Сёма в это время
// работает, а читателей SQLite в режиме WAL пускает сколько угодно.

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/engine"
)

func measureClasses(conf *config.Config) int {
	db, err := sql.Open("sqlite", "file:"+conf.DB+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Println("база не открылась:", err)
		return 1
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, class, outside, model, consumer, COALESCE(prompt, ''), COALESCE(preview, '')
		FROM sent_log ORDER BY id`)
	if err != nil {
		fmt.Println("журнал не читается:", err)
		return 1
	}
	defer rows.Close()

	total := 0
	before := map[string]int{}
	after := map[string]int{}
	changed := 0
	secretOutside := 0
	byReason := map[string]int{}
	examples := []string{}

	for rows.Next() {
		var id int64
		var class, model, consumer, prompt, preview string
		var outside int
		if rows.Scan(&id, &class, &outside, &model, &consumer, &prompt, &preview) != nil {
			continue
		}
		text := prompt
		if strings.TrimSpace(text) == "" {
			text = preview
		}
		total++
		before[class]++
		fresh, reason := engine.TextClass(text)
		after[fresh]++
		if fresh != class {
			changed++
			byReason[reason]++
			if fresh == "секретное" && len(examples) < 5 {
				examples = append(examples, fmt.Sprintf("#%d %s → %s (%s), потребитель %s, модель %s",
					id, class, fresh, reason, shortConsumer(consumer), model))
			}
		}
		if fresh == "секретное" && outside == 1 {
			secretOutside++
		}
	}

	fmt.Printf("Записей в журнале: %d\n\n", total)
	fmt.Println("Класс сейчас:")
	printCounts(before, total)
	fmt.Println("\nКласс по новым приметам:")
	printCounts(after, total)
	fmt.Printf("\nИзменилось записей: %d (%.1f%%)\n", changed, share(changed, total))
	if len(byReason) > 0 {
		fmt.Println("Из-за чего:")
		printCounts(byReason, changed)
	}
	fmt.Printf("\nБыло бы отклонено (секретное к недоверенной модели): %d\n", secretOutside)
	if len(examples) > 0 {
		fmt.Println("\nПримеры новых секретных (без содержимого):")
		for _, e := range examples {
			fmt.Println(" ", e)
		}
	}
	return 0
}

func printCounts(m map[string]int, total int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return m[keys[i]] > m[keys[j]] })
	for _, k := range keys {
		name := k
		if name == "" {
			name = "(пусто)"
		}
		fmt.Printf("  %-46s %5d  %4.1f%%\n", name, m[k], share(m[k], total))
	}
}

func share(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) * 100 / float64(b)
}

func shortConsumer(s string) string {
	if s == "" {
		return "(не назван)"
	}
	return s
}
