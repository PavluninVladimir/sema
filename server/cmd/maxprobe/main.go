// maxprobe — проба связи с MAX: соединение, INIT и ничего больше. Кода на
// телефон не запрашивает. Нужна, чтобы отличить «узел с просроченным
// сертификатом» от «сети нет» и «протокол не тот» — из контейнера видно
// не то же самое, что с машины.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/PavluninVladimir/sema/server/internal/max"
)

func main() {
	relaxed := flag.Bool("relaxed", false, "принимать просроченный сертификат")
	phone := flag.String("phone", "", "номер для запроса кода: на него уйдёт SMS")
	opcode := flag.Int("opcode", 0, "разовый запрос своим опкодом")
	tries := flag.Int("n", 3, "сколько раз пробовать")
	flag.Parse()
	for i := 1; i <= *tries; i++ {
		cl := max.New(fmt.Sprintf("проба-%d", time.Now().UnixNano()), "")
		cl.AllowExpiredCert(*relaxed)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := cl.Hello(ctx)
		cancel()
		cl.Close()
		if err != nil {
			fmt.Printf("попытка %d: не вышло — %v\n", i, err)
			continue
		}
		fmt.Printf("попытка %d: соединение и INIT прошли\n", i)
		if *phone != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			op := *opcode
			var payload any = map[string]any{"phone": *phone, "type": "START_AUTH", "language": "ru"}
			if op == 0 {
				op = 17
			} else {
				payload = map[string]any{"source": "auth", "identifier": *phone}
			}
			out, err := cl.RawCall(ctx, op, payload)
			cancel()
			if err != nil {
				fmt.Printf("опкод %d: отказ — %v\n", op, err)
			} else {
				fmt.Printf("опкод %d: ответ — %s\n", op, out)
			}
		}
		os.Exit(0)
	}
	os.Exit(1)
}
