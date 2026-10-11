package api

// Права собеседников матрицей: «чат × право».
//
// До 2 октября 2026 у каждого чата Telegram была строка с раскрытием, а в нём
// семь тумблеров и класс — шестнадцать собеседников давали шестнадцать
// раскрытий по семь строк. Права у всех одни и те же, и человек сравнивает
// чаты между собой («кому разрешено отвечать»), а не читает один: это матрица,
// а не список. Строка — чат (у группы с темами темы вложены под ней), колонка —
// право, клетка переключает его тем же действием, что был тумблер.
//
// Права, которых у такого чата не бывает (админом — только личный чат,
// публиковать — только в канал, отвечать каналу некому), — не тумблер, который
// откажет после нажатия, а клетка «na» с причиной в подсказке. Сама матрица
// ничего не решает: она рисует то, что ей дали, и зовёт те же действия, что
// звали тумблеры (`tg_chat_right`, `max_chat_right`), — проверки прав остались
// в них, и прямой вызов запрещённого действия отказывает так же, как нажатие.
//
// Один код на Telegram и MAX: у MAX колонок меньше, строка собирается так же.

import "fmt"

// rightsColumn — колонка матрицы: слово в шапке и ключ, по которому строка
// называет свою клетку.
type rightsColumn struct{ ID, Title string }

// rightsRow — строка матрицы. Клетки названы ключом колонки: строка не обязана
// знать порядок колонок, а колонка без клетки — «na».
type rightsRow struct {
	ID, Title, Caption string
	Badges             []Badge
	Level              string
	// Nested — вложенная строка: тема группы под группой
	Nested bool
	Cells  map[string]Cell
	// Menu — «···» строки: у темы это «Как у группы»
	Menu []Action
}

// rightsMatrix — матрица прав из колонок и строк. Подпись под ней говорит, как
// читать клетки: нажатие переключает, прочерк — права не бывает.
func rightsMatrix(id, title, caption string, columns []rightsColumn, rows []rightsRow) Component {
	m := Component{Type: "matrix", ID: id, Title: title, Caption: caption, Style: "rights"}
	for _, col := range columns {
		m.Columns = append(m.Columns, col.Title)
	}
	for _, r := range rows {
		row := Component{Type: "row", ID: r.ID, Subtitle: r.Title, Caption: r.Caption, Badges: r.Badges, Menu: r.Menu}
		if r.Nested {
			row.Style = "nested"
		}
		if r.Level != "" {
			withLevel(&row, r.Level)
		}
		for _, col := range columns {
			cell, has := r.Cells[col.ID]
			if !has {
				cell = rightNA("для этого чата права «" + col.Title + "» нет")
			}
			row.Cells = append(row.Cells, cell)
		}
		m.Children = append(m.Children, row)
	}
	return m
}

// rightCell — клетка права: включено или нет и действие, которое переключает.
// Подсказка называет чат, право и состояние — коротко: матрица читается
// глазами, а скринридер слышит только подсказку. Что право значит, стоит один
// раз под матрицей, а не в каждой клетке.
func rightCell(who, right string, on bool, act Action) Cell {
	cell := Cell{State: "off", Action: act}
	cell.Title = who + " · " + right + ": выключено"
	if on {
		cell.State = "on"
		cell.Title = who + " · " + right + ": включено"
	}
	return cell
}

// rightNA — клетка «права не бывает»: не кнопка, причина — в подсказке.
func rightNA(reason string) Cell { return Cell{State: "na", Title: reason} }

// matrixPage — какую порцию строк показать: размер порции по умолчанию либо
// предел, который клиент удвоил кнопкой «Показать ещё».
func matrixPage(limit, def int) int {
	if limit > 0 && limit != defaultRows {
		return limit
	}
	return def
}

// moreRows — кнопка «Показать ещё» под матрицей: сколько показано и сколько
// нашлось. Клиент удвоит предел экрана и сохранит отбор и поиск.
func moreRows(shown, matched int) Component {
	return Component{Type: "button", Actions: []Action{
		{Action: "$more", Label: fmt.Sprintf("Показать ещё · %d из %d", shown, matched)}}}
}
