package web

// Комментарии вон из оболочки (perf-pass 6.1). Комментарии в app.js и
// style.css — русские, по два байта на букву, и gzip их почти не берёт: со
// своими комментариями app.js сжимается до 58 КБ, без них — до 28. Их
// читают в исходнике, а не в браузере; в браузер едет код.
//
// Разбор простой, без зависимостей и без node: строки, шаблоны с ${…},
// регулярные выражения и комментарии. Переводы строк сохраняются — номер
// строки в ошибке браузера совпадает с исходником.

import "bytes"

// regexBefore — после каких знаков «/» начинает регулярное выражение, а не
// деление: после открывающей скобки, запятой, оператора, «;», «{», «}» и
// в начале. После «)», «]», имени и числа — деление.
const regexBefore = "(,=:[!&|?{};+-*%<>~^"

// regexAfterWord — слова, после которых «/» — тоже регулярное выражение.
var regexAfterWord = map[string]bool{
	"return": true, "typeof": true, "instanceof": true, "in": true, "of": true, "new": true,
	"delete": true, "void": true, "throw": true, "case": true, "do": true, "else": true,
	"yield": true, "await": true,
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// stripJS — код без комментариев.
func stripJS(src []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(src))
	n := len(src)
	// стек шаблонов: глубина фигурных скобок внутри каждого открытого ${…}
	var tmpl []int
	prev := byte(0)  // последний значимый знак кода (не пробел)
	prev2 := byte(0) // знак перед ним: «++» и «--» перед «/» — деление
	word := ""       // последнее слово кода, если последним было слово
	gap := false     // после последнего значимого знака был пробел
	i := 0
	// copyString — строка в кавычках q до закрывающей (с экранированием)
	copyString := func(q byte) {
		out.WriteByte(q)
		i++
		for i < n {
			c := src[i]
			out.WriteByte(c)
			i++
			if c == '\\' && i < n {
				out.WriteByte(src[i])
				i++
				continue
			}
			if c == q || c == '\n' {
				return
			}
		}
	}
	// copyTemplate — шаблон с начала или с конца ${…}: до «`» или до «${»
	copyTemplate := func() {
		for i < n {
			c := src[i]
			if c == '\\' && i+1 < n {
				out.WriteByte(c)
				out.WriteByte(src[i+1])
				i += 2
				continue
			}
			if c == '`' {
				out.WriteByte(c)
				i++
				prev, word = '`', ""
				return
			}
			if c == '$' && i+1 < n && src[i+1] == '{' {
				out.WriteString("${")
				i += 2
				tmpl = append(tmpl, 0)
				prev, word = '{', ""
				return
			}
			out.WriteByte(c)
			i++
		}
	}
	for i < n {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i < n && !(src[i] == '*' && i+1 < n && src[i+1] == '/') {
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			i += 2
			// комментарий разделял слова — пробел вместо него, иначе «a/**/b»
			// склеилось бы в «ab»
			out.WriteByte(' ')
			continue
		case c == '\'' || c == '"':
			copyString(c)
			prev, word = c, ""
			continue
		case c == '`':
			out.WriteByte(c)
			i++
			copyTemplate()
			continue
		case c == '/':
			regex := prev == 0 || bytes.IndexByte([]byte(regexBefore), prev) >= 0 || regexAfterWord[word]
			if (prev == '+' && prev2 == '+') || (prev == '-' && prev2 == '-') {
				regex = false // «i++ / 2» — деление
			}
			if !regex {
				out.WriteByte(c)
				i++
				prev, word = c, ""
				continue
			}
			// регулярное выражение: до «/» вне [...], с экранированием, и флаги
			out.WriteByte(c)
			i++
			inClass := false
			for i < n {
				d := src[i]
				out.WriteByte(d)
				i++
				if d == '\\' && i < n {
					out.WriteByte(src[i])
					i++
					continue
				}
				if d == '[' {
					inClass = true
				} else if d == ']' {
					inClass = false
				} else if d == '/' && !inClass {
					break
				} else if d == '\n' {
					break
				}
			}
			for i < n && isIdent(src[i]) {
				out.WriteByte(src[i])
				i++
			}
			prev, word = '/', ""
			continue
		case c == '{':
			if len(tmpl) > 0 {
				tmpl[len(tmpl)-1]++
			}
		case c == '}':
			if len(tmpl) > 0 {
				if tmpl[len(tmpl)-1] == 0 {
					// конец ${…}: обратно в шаблон
					tmpl = tmpl[:len(tmpl)-1]
					out.WriteByte(c)
					i++
					copyTemplate()
					continue
				}
				tmpl[len(tmpl)-1]--
			}
		}
		out.WriteByte(c)
		i++
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			// пробел значимым знаком не считается, но слово им кончается:
			// «return x» — два слова, а не «returnx»
			gap = true
		case isIdent(c):
			if prev != 0 && isIdent(prev) && !gap {
				word += string(c)
			} else {
				word = string(c)
			}
			prev2, prev, gap = prev, c, false
		default:
			prev2, prev, word, gap = prev, c, "", false
		}
	}
	return out.Bytes()
}

// stripCSS — стили без комментариев (строки в кавычках не трогаются).
func stripCSS(src []byte) []byte {
	var out bytes.Buffer
	out.Grow(len(src))
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i < n && !(src[i] == '*' && i+1 < n && src[i+1] == '/') {
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			i += 2
			continue
		case c == '\'' || c == '"':
			out.WriteByte(c)
			i++
			for i < n {
				d := src[i]
				out.WriteByte(d)
				i++
				if d == '\\' && i < n {
					out.WriteByte(src[i])
					i++
					continue
				}
				if d == c || d == '\n' {
					break
				}
			}
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.Bytes()
}
