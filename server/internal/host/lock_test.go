package host

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Два сервера на одном каталоге — это повреждённая база, а не «второй
// сервер». Второй обязан получить отказ, а не начать работу.
func TestSecondInstanceRejected(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatalf("первый не смог занять: %v", err)
	}
	defer first.Release()

	if _, err := Acquire(dir); err == nil {
		t.Fatal("второй экземпляр занял тот же каталог")
	} else if !strings.Contains(err.Error(), "занят") {
		t.Fatalf("причина отказа непонятная: %v", err)
	}

	// в записи видно, кто держит: это только для человека, решений по ней нет
	holder := Owner(dir)
	if holder.PID != os.Getpid() {
		t.Fatalf("в записи чужой процесс: %+v", holder)
	}
	if !strings.Contains(holder.String(), "процесс") {
		t.Fatalf("строка держателя не читается: %q", holder.String())
	}

	// отпустили — можно снова
	first.Release()
	second, err := Acquire(dir)
	if err != nil {
		t.Fatalf("после освобождения не занялось: %v", err)
	}
	second.Release()
}

// Убитый процесс отпускает замок сам: логика «забрать у мёртвого» не нужна
// и была бы источником ошибок. Проверяем настоящим убийством — тест
// перезапускает сам себя, чтобы не зависеть от сборки на месте.
func TestKilledProcessReleasesLock(t *testing.T) {
	if dir := os.Getenv("SEMA_LOCK_HOLD"); dir != "" {
		// это подпроцесс: берём замок и ждём, пока нас убьют
		z, err := Acquire(dir)
		if err != nil {
			os.Exit(1)
		}
		_ = z
		fmt.Println("взял")
		os.Stdout.Sync()
		time.Sleep(time.Minute)
		return
	}
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run", "TestKilledProcessReleasesLock", "-test.v")
	cmd.Env = append(os.Environ(), "SEMA_LOCK_HOLD="+dir)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// ждём строку «взял»: перед ней подпроцесс печатает служебные строки
	// самого тестового каркаса
	took := false
	scan := bufio.NewScanner(out)
	for scan.Scan() {
		if strings.Contains(scan.Text(), "взял") {
			took = true
			break
		}
	}
	if !took {
		_ = cmd.Process.Kill()
		t.Fatal("подпроцесс не взял замок")
	}
	if _, err := Acquire(dir); err == nil {
		_ = cmd.Process.Kill()
		t.Fatal("замок не держится, пока жив другой процесс")
	}
	// убиваем насмерть, как это делает docker kill
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	z, err := Acquire(dir)
	if err != nil {
		t.Fatalf("после убийства процесса замок не освободился: %v", err)
	}
	z.Release()
}
