package web

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Разбор не путает комментарий с тем, что на него похоже: «//» в строке и в
// регулярном выражении, «/*» в шаблоне, деление и регулярное выражение.
func TestStripJSKeepsCode(t *testing.T) {
	cases := map[string]string{
		"a = 1; // комментарий\nb = 2;":                                     "a = 1; \nb = 2;",
		"x = '//не комментарий'; // а это — да":                             "x = '//не комментарий'; ",
		"u = \"http://пример\"; /* блок\nдва */ y":                          "u = \"http://пример\"; \n  y",
		"if (/^a\\/b/.test(s)) ok(); // хвост":                              "if (/^a\\/b/.test(s)) ok(); ",
		"r = s.replace(/[/*]+/g, '');":                                      "r = s.replace(/[/*]+/g, '');",
		"z = a / b / c; // деление":                                         "z = a / b / c; ",
		"q = `шаблон // не комментарий ${a + `в ${b}`} /* и это */`; // да": "q = `шаблон // не комментарий ${a + `в ${b}`} /* и это */`; ",
		"return /x/.test(s) // вот":                                         "return /x/.test(s) ",
		"o = {a: 1}; /* x */ w = n / 2;":                                    "o = {a: 1};   w = n / 2;",
		// ревью perf-pass 6: слово кончается пробелом, «++» перед «/» — деление
		"if (a) return x / 2 / 3; // хвост":            "if (a) return x / 2 / 3; ",
		"if (a) {} else return /re/.test(s); // хвост": "if (a) {} else return /re/.test(s); ",
		"n = i++ / 2; // хвост":                        "n = i++ / 2; ",
	}
	for in, want := range cases {
		if got := string(stripJS([]byte(in))); got != want {
			t.Errorf("stripJS(%q)\n  = %q\n  ждали %q", in, got, want)
		}
	}
}

func TestStripCSSKeepsStrings(t *testing.T) {
	in := "a { content: \"/* не комментарий */\"; } /* комментарий\nдва */ b { x: url('a//b'); }"
	want := "a { content: \"/* не комментарий */\"; } \n b { x: url('a//b'); }"
	if got := string(stripCSS([]byte(in))); got != want {
		t.Errorf("stripCSS\n  = %q\n  ждали %q", got, want)
	}
}

// Настоящие файлы оболочки после чистки — всё ещё правильный JavaScript, и
// строк столько же (номера строк в ошибках браузера совпадают с исходником).
// Проверяет node, если он есть на машине; браузерные тесты гоняют ту же
// очищенную оболочку в Chrome.
func TestStrippedShellStillParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("нет node — синтаксис очищенного клиента проверят браузерные тесты")
	}
	for _, name := range []string{"app.js", "sw.js"} {
		src, err := files.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		got := stripJS(src)
		if a, b := strings.Count(string(src), "\n"), strings.Count(string(got), "\n"); a != b {
			t.Errorf("%s: строк было %d, стало %d — номера строк в ошибках разойдутся", name, a, b)
		}
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(node, "--check", path).CombinedOutput(); err != nil {
			t.Errorf("%s после чистки не разбирается: %v\n%s", name, err, out)
		}
		t.Logf("%s: %d → %d байт", name, len(src), len(got))
	}
}
