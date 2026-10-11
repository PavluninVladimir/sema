package api

// Вес экрана «Скиллы» на данных настоящего размера. До переделки полный текст
// каждого скилла ехал в кнопке «Скопировать» каждой строки списка: четыре
// встроенных скилла — 51 КБ чистого текста, и тот же текст целиком лежал в
// разметке (разбор 1 октября 2026, `design/nastroyki-2/RAZBOR.md`).
//
// Замер «до» (HEAD 3aaf000, четыре встроенных скилла, разметка как уходит в браузер):
//
//	список 333 узла · 78,1 КБ; экран открытого скилла 356 узлов · 110,8 КБ;
//	«Добавить скилл» — тот же список с формой под ним, 333 · 78,1 КБ
//
// Пустая страница настроек без содержимого раздела — 266 узлов и 23 КБ (оболочка и
// колонка разделов): список скиллов сам по себе весил 67 узлов и 55 КБ.

import (
	"fmt"
	"strings"
	"testing"
)

// Вес списка не зависит от размера текстов: на четырёх встроенных скиллах (51 КБ
// текста) список весит немногим больше пустой страницы настроек.
func TestSkillsListWeightIsUnderBudget(t *testing.T) {
	s, _ := testServer(t)
	var out strings.Builder
	fmt.Fprintf(&out, "\n%-34s %7s %8s\n", "экран", "узлов", "КБ")
	weights := map[string]screenWeight{}
	for _, screen := range []string{skillsScreen, skillsScreen + "@linux-admin", skillsScreen + "@new",
		storagesScreen, storagesScreen + "~" + storTabSources, toolsScreen,
		notifySettingsScreen, notifySettingsScreen + "~" + notifyTabRules} {
		w := weigh(t, s, screen)
		weights[screen] = w
		fmt.Fprintf(&out, "%-34s %7d %8.1f\n", screen, w.Nodes, float64(w.Bytes)/1024)
	}
	t.Log(out.String())
	// «Добавить скилл» — страница без содержимого раздела: это и есть оболочка
	shell := weights[skillsScreen+"@new"]
	list := weights[skillsScreen]
	if own := list.Bytes - shell.Bytes; own > 12*1024 {
		t.Errorf("список скиллов сам весит %d байт при пустой оболочке %d: тексты опять едут в разметке", own, shell.Bytes)
	}
	if list.Bytes > 36*1024 {
		t.Errorf("список скиллов %d байт: до переделки было 78 КБ, а должно быть около 27", list.Bytes)
	}
	// экран скилла несёт текст один раз: поле формы, а не поле и ещё кнопка копирования
	one := weights[skillsScreen+"@linux-admin"]
	if one.Bytes > 70*1024 {
		t.Errorf("экран скилла %d байт: текст едет больше одного раза", one.Bytes)
	}
}
