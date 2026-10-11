package engine

import (
	"regexp"
	"strings"
)

var simpleAwk = regexp.MustCompile(`^\{\s*print\s+\$[0-9]+(?:\s*,\s*\$[0-9]+)*\s*\}$`)
var simpleSed = regexp.MustCompile(`^[0-9]+(?:,[0-9$]+)?p$`)
var harmlessRedirect = regexp.MustCompile(`(?:^|[ \t])(?:2>&1|[012]?>>?[ \t]*/dev/null)(?:[ \t]|$)`)

// literalShellWords разбирает только буквальные аргументы. Расширения
// оболочки, подстановки, escape-последовательности и неполные кавычки
// требуют человека: регулярные выражения не заменяют интерпретатор shell.
func literalShellWords(part string) ([]string, bool) {
	words, _, ok := literalShellWordsQ(part)
	return words, ok
}

// literalShellWordsQ — те же слова и признак «слово пришло из кавычек».
// Признак нужен ровно одному месту: перенаправления в кавычках не бывает.
// «awk '$3>=1000'» — это сравнение внутри программы awk, а не запись в
// файл, и требовать за него человека значит просить подтверждения на
// чтение.
func literalShellWordsQ(part string) ([]string, []bool, bool) {
	var quoted []bool
	inQuotes := false
	var words []string
	var word strings.Builder
	var quote rune
	started := false
	flush := func() {
		if started {
			words = append(words, word.String())
			quoted = append(quoted, inQuotes)
			word.Reset()
			started = false
			inQuotes = false
		}
	}
	for _, c := range part {
		if c == 0 || c == '\r' {
			return nil, nil, false
		}
		if quote == '\'' {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		if c == '$' || c == '`' || c == '\\' {
			return nil, nil, false
		}
		if quote == '"' {
			if c == quote {
				quote = 0
			} else {
				word.WriteRune(c)
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			started = true
			inQuotes = true
		case ' ', '\t':
			flush()
		case '#', '(', ')', '[', ']', '{', '}', '<':
			return nil, nil, false
		default:
			word.WriteRune(c)
			started = true
		}
	}
	if quote != 0 {
		return nil, nil, false
	}
	flush()
	return words, quoted, true
}

// shellSafety дополняет пользовательский справочник чтения. Неясный
// синтаксис нельзя одобрить ни совпадением префикса, ни решением модели.
func shellSafety(cmd string) string {
	const reason = "аргументы или синтаксис команды требуют подтверждения человека"
	for _, part := range splitCommand(cmd) {
		words, quoted, ok := literalShellWordsQ(part)
		if !ok {
			return reason
		}
		if len(words) == 0 {
			continue
		}
		// перенаправление ищем только среди НЕзакавыченных слов: «>» внутри
		// кавычек — это текст, а не запись в файл. «awk '$3>=1000'»
		// уходило к человеку за сравнение чисел
		var outside []string
		for i, w := range words {
			if !quoted[i] {
				outside = append(outside, w)
			}
		}
		// В bash «>&file» тоже записывает файл. Старый шаблон видел
		// только обычный «> file» и пропускал объединённый stdout/stderr.
		if strings.Contains(harmlessRedirect.ReplaceAllString(strings.Join(outside, " "), " "), ">") {
			return "перенаправление требует подтверждения человека"
		}
		// безобидное «2>/dev/null» — не аргумент команды: у «crontab -l
		// 2>/dev/null» оно считалось вторым аргументом, и разрешённая
		// команда чтения уходила к человеку
		words = withoutHarmlessRedirects(words)
		if len(words) == 0 {
			continue
		}
		canonical := strings.Join(words, " ")
		if why := mutatingKey(canonical); why != "" {
			return why
		}
		args := words[1:]
		switch words[0] {
		case "awk", "gawk", "mawk", "nawk":
			if len(args) > 0 && strings.HasPrefix(args[0], "-F") && len(args[0]) > 2 {
				args = args[1:]
			}
			if len(args) == 0 || !simpleAwk.MatchString(args[0]) {
				return reason
			}
			for _, a := range args[1:] {
				if strings.HasPrefix(a, "-") || strings.Contains(a, "=") {
					return reason
				}
			}
		case "sed":
			if len(args) < 2 || args[0] != "-n" || !simpleSed.MatchString(args[1]) {
				return reason
			}
			for _, a := range args[2:] {
				if strings.HasPrefix(a, "-") {
					return reason
				}
			}
		case "sort":
			for _, a := range args {
				if strings.HasPrefix(a, "--o") || strings.HasPrefix(a, "--compress") || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "o")) {
					return reason
				}
			}
		case "sysctl":
			for _, a := range args {
				if strings.Contains(a, "=") {
					return reason
				}
				if strings.HasPrefix(a, "-") && a != "-a" && a != "-n" && a != "-e" && a != "--all" && a != "--values" {
					return reason
				}
			}
		case "mount":
			if len(args) != 0 && !(len(args) == 1 && args[0] == "-l") {
				return reason
			}
		case "ip":
			for len(args) > 0 && strings.HasPrefix(args[0], "-") {
				switch args[0] {
				case "-br", "-brief", "-4", "-6", "-o", "-oneline", "-s", "-d", "-j", "-json":
				default:
					return reason
				}
				args = args[1:]
			}
			if len(args) == 0 {
				return reason
			}
			switch args[0] {
			case "addr", "address", "link", "route", "rule", "neigh", "neighbor":
			default:
				return reason
			}
			if len(args) > 1 && args[1] != "show" && args[1] != "list" {
				return reason
			}
			for _, a := range args {
				if strings.HasPrefix(a, "-") {
					return reason
				}
			}
		case "apt", "apt-get", "apt-cache":
			for _, a := range args {
				if strings.Contains(a, "::") || strings.HasPrefix(a, "--option") || strings.HasPrefix(a, "--config") || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "oc")) {
					return reason
				}
			}
		case "dpkg", "dpkg-query":
			for _, a := range args {
				if strings.Contains(a, "invoke") || strings.HasPrefix(a, "--command") {
					return reason
				}
			}
		case "hostname":
			for _, a := range args {
				switch a {
				case "-f", "-s", "-d", "-i", "-I", "--fqdn", "--short":
				default:
					return reason
				}
			}
		case "hostnamectl", "timedatectl":
			if len(args) > 1 || (len(args) == 1 && args[0] != "status" && args[0] != "show") {
				return reason
			}
		case "date":
			for _, a := range args {
				if a != "-u" && a != "--utc" && !strings.HasPrefix(a, "+") {
					return reason
				}
			}
		case "dmesg":
			for _, a := range args {
				switch a {
				case "-T", "-r", "-t", "-x", "--ctime", "--raw", "--decode", "--notime":
				default:
					return reason
				}
			}
		case "journalctl":
			for _, a := range args {
				for _, p := range []string{"--vac", "--rot", "--fl", "--sy", "--setup", "--rel", "--update", "--smart"} {
					if strings.HasPrefix(a, p) {
						return reason
					}
				}
			}
		case "ss":
			for _, a := range args {
				if strings.HasPrefix(a, "-") && (strings.Contains(a, "K") || strings.HasPrefix(a, "--k")) {
					return reason
				}
			}
		case "crontab":
			if len(args) != 1 || args[0] != "-l" {
				return reason
			}
		}
		if strings.HasPrefix(words[0], "/") {
			for _, a := range args {
				if strings.Contains(a, "file=") || strings.Contains(a, "on-error=") {
					return reason
				}
			}
		}
	}
	return ""
}

// withoutHarmlessRedirects — убрать «2>/dev/null», «>/dev/null» и
// «2>&1» из списка слов. Они ничего не пишут и не должны считаться
// аргументами команды.
func withoutHarmlessRedirects(words []string) []string {
	out := words[:0:0]
	for _, w := range words {
		if harmlessRedirect.MatchString(" " + w + " ") {
			continue
		}
		out = append(out, w)
	}
	return out
}
