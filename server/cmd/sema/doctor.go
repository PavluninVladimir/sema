package main

// Самоосмотр: `sema-server -doctor`. Главное свойство — он работает и
// когда Сёма не поднимается. Поэтому здесь нет ни движка, ни моделей.
//
// Сами вопросы живут не здесь, а в `internal/selfcheck`: у самоосмотра две
// двери — эта и наблюдатель у живого сервера, — и описание у них одно.
// Порознь они разъезжались молча: сертификат кончается на работающей Сёме, а
// спрашивал о нём только запуск. Здесь остаётся то, что дверь и должна
// делать: собрать среду, задать вопросы своей стороны и напечатать ответы.

import (
	"fmt"
	"os"

	"github.com/PavluninVladimir/sema/server/internal/config"
	"github.com/PavluninVladimir/sema/server/internal/secret"
	"github.com/PavluninVladimir/sema/server/internal/selfcheck"
	"github.com/PavluninVladimir/sema/server/internal/store"
	"github.com/PavluninVladimir/sema/server/internal/version"
	"time"
)

func inspect(conf *config.Config) int {
	env := selfcheck.Env{Conf: conf, Key: os.Getenv(conf.KeyEnvName())}

	// база открывается один раз на весь осмотр; не открылась — проверки,
	// которым она нужна, скажут об этом сами, а не соврут «ок»
	if _, err := os.Stat(conf.DB); err == nil {
		if st, err := store.Open(conf.DB); err != nil {
			fmt.Printf("[ беда ] %-20s не открывается: %v\n", "База", err)
			fmt.Println("                              → проверьте права на файл и свободное место")
		} else {
			// ключ хранилищу даёт движок; здесь движка нет, поэтому даём
			// сами — иначе осмотр всегда говорил бы «ключ не задан»
			if box, berr := secret.New(env.Key); berr == nil && box != nil {
				st.UseSecretBox(box)
			}
			env.DB = st
			defer st.Close()
		}
	}

	results := selfcheck.Collect(selfcheck.AtStartup, env)

	troubles := 0
	fmt.Printf("Самоосмотр Сёмы · сборка %s · %s\n\n", version.String(), time.Now().Format("2006-01-02 15:04"))
	for _, r := range results {
		mark := "  ок  "
		switch {
		case r.Trouble:
			mark = " беда "
			troubles++
		case r.Warn:
			mark = " вним "
		}
		fmt.Printf("[%s] %-20s %s\n", mark, r.Name, r.Line)
		if r.Advice != "" && (r.Trouble || r.Warn) {
			fmt.Printf("                              → %s\n", r.Advice)
		}
	}
	fmt.Println()
	if troubles == 0 {
		fmt.Println("Всё в порядке: можно запускаться.")
		return 0
	}
	fmt.Printf("Мешает запуску: %d\n", troubles)
	return 1
}
