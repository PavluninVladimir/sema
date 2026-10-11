package engine

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
)

// Списки модулей и моделей живут разобранными в памяти (perf-pass 2): запись
// через хранилище мимо движка — разовая миграция, восстановление — движок
// видит на следующем же чтении, без перезапуска.
func TestListCachesSeeWritesPastEngine(t *testing.T) {
	e, st := newTestEngine(t, "", nil)
	if err := e.InstallModule(&Module{Name: "proba", Title: "Проба", Code: "function run() { return 1; }"}); err != nil {
		t.Fatal(err)
	}
	if m, ok := e.Module("proba"); !ok || m.Title != "Проба" {
		t.Fatalf("модуль не прочитался: %+v", m)
	}
	// мимо движка: правим строку в базе
	all := map[string]*Module{}
	_ = json.Unmarshal([]byte(st.Setting(modulesKey, "{}")), &all)
	all["proba"].Title = "Проба мимо движка"
	raw, _ := json.Marshal(all)
	if err := st.SetSetting(modulesKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.Module("proba"); m.Title != "Проба мимо движка" {
		t.Errorf("движок отдал старое: %q", m.Title)
	}

	if err := e.AddModel(ModelConf{Name: "проба-модель", Type: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "x"}); err != nil {
		t.Fatal(err)
	}
	list, _ := e.userModelsE()
	var models []ModelConf
	_ = json.Unmarshal([]byte(st.Setting(modelsKey, "[]")), &models)
	for i := range models {
		if models[i].Name == "проба-модель" {
			models[i].Context = 12345
		}
	}
	raw, _ = json.Marshal(models)
	if err := st.SetSetting(modelsKey, string(raw)); err != nil {
		t.Fatal(err)
	}
	list, _ = e.userModelsE()
	found := false
	for _, c := range list {
		if c.Name == "проба-модель" && c.Context == 12345 {
			found = true
		}
	}
	if !found {
		t.Error("движок не увидел модель, записанную мимо него")
	}
}

// Правка модуля, которую вызывающий сделал на выданной копии и не сохранил,
// кэш не портит: следующий читатель видит то, что в базе.
func TestModuleCacheNotSpoiledByCaller(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.InstallModule(&Module{Name: "proba", Title: "Проба", Code: "function run() { return 1; }", Needs: []string{"http"}}); err != nil {
		t.Fatal(err)
	}
	all, err := e.modulesMap()
	if err != nil {
		t.Fatal(err)
	}
	all["proba"].Title = "испорчено"
	all["proba"].Needs[0] = "всё"
	all["proba"].On = map[string]string{"x": "y"}
	delete(all, "proba")
	if m, ok := e.Module("proba"); !ok || m.Title != "Проба" || len(m.Needs) != 1 || m.Needs[0] != "http" || m.On != nil {
		t.Errorf("кэш испорчен вызывающим: %+v", m)
	}
}

// Копия модуля не делит с исходным ни одного среза и словаря. Новое поле
// такого вида без строки в clone() роняет этот тест, а не кэш у человека.
func TestModuleCloneIsDeep(t *testing.T) {
	var m Module
	v := reflect.ValueOf(&m).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Slice:
			f.Set(reflect.MakeSlice(f.Type(), 1, 1))
		case reflect.Map:
			mp := reflect.MakeMap(f.Type())
			mp.SetMapIndex(reflect.New(f.Type().Key()).Elem(), reflect.New(f.Type().Elem()).Elem())
			f.Set(mp)
		case reflect.Pointer, reflect.Interface, reflect.Chan, reflect.Func:
			t.Errorf("поле %s вида %s: clone() его не копирует глубоко", v.Type().Field(i).Name, f.Kind())
		case reflect.Struct:
			t.Errorf("поле %s — структура: проверьте, нет ли внутри срезов", v.Type().Field(i).Name)
		}
	}
	c := m.clone()
	cv := reflect.ValueOf(c).Elem()
	for i := 0; i < v.NumField(); i++ {
		f, g := v.Field(i), cv.Field(i)
		name := v.Type().Field(i).Name
		switch f.Kind() {
		case reflect.Slice:
			if f.Pointer() == g.Pointer() {
				t.Errorf("срез %s у копии общий с исходным", name)
			}
			// элементы срезов — только значения без ссылок
			if k := f.Type().Elem().Kind(); k == reflect.Struct {
				et := f.Type().Elem()
				for j := 0; j < et.NumField(); j++ {
					switch et.Field(j).Type.Kind() {
					case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
						t.Errorf("%s: у элемента поле %s со ссылкой — slices.Clone его не скопирует", name, et.Field(j).Name)
					}
				}
			}
		case reflect.Map:
			if f.Pointer() == g.Pointer() {
				t.Errorf("словарь %s у копии общий с исходным", name)
			}
		}
	}
}

// Чтение и запись списков из многих горутин: под -race без гонок, и после
// последней записи читатель видит её.
func TestListCachesRace(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.InstallModule(&Module{Name: "proba", Title: "Проба", Code: "function run() { return 1; }"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if i%4 == 0 {
					all, err := e.modulesMap()
					if err == nil {
						all["proba"].About = "правка"
						_ = e.saveModules(all)
					}
					continue
				}
				_ = e.Modules()
				_, _ = e.Module("proba")
				_ = e.userModels()
				_ = e.Providers()
			}
		}(i)
	}
	wg.Wait()
	if m, _ := e.Module("proba"); m.About != "правка" {
		t.Errorf("после записи читается %q", m.About)
	}
}

// Module отдаёт копию одного модуля из общего кэша: правка выданного не
// видна следующему читателю; модель по имени ищется по индексу и видит
// только что добавленную (perf-pass 3).
func TestSharedCachesHandOutCopies(t *testing.T) {
	e, _ := newTestEngine(t, "", nil)
	if err := e.InstallModule(&Module{Name: "proba", Title: "Проба", Code: "function run() { return 1; }", Needs: []string{"http"}}); err != nil {
		t.Fatal(err)
	}
	m, _ := e.Module("proba")
	m.Title, m.Needs[0] = "испорчено", "всё"
	if again, _ := e.Module("proba"); again.Title != "Проба" || again.Needs[0] != "http" {
		t.Errorf("правка выданного модуля испортила кэш: %+v", again)
	}
	if e.ModelIsRemote("новая") != true || e.PriceOf("новая") != "" {
		t.Error("незнакомая модель: удалённая и без цены")
	}
	if err := e.AddModel(ModelConf{Name: "новая", Type: "openai", BaseURL: "http://llm:8080/v1", Model: "x", Price: "$1 / 1M"}); err != nil {
		t.Fatal(err)
	}
	if e.ModelIsRemote("новая") || e.PriceOf("новая") != "$1 / 1M" {
		t.Errorf("только что добавленная модель не видна по индексу: удалённая=%v цена=%q", e.ModelIsRemote("новая"), e.PriceOf("новая"))
	}
}
