package web

// Сам шаблон разметки. Вынесен отдельным файлом, чтобы он читался как
// разметка, а не как строка посреди кода.

const markupTemplate = `
<aside id="menu" aria-label="Разделы"
  hx-get="/ui?screen={{.Addr | urlquery}}" hx-select="#menu" hx-target="#menu" hx-swap="outerHTML" hx-trigger="sema:menu"
  data-waiting="{{.Waiting}}">
  <!-- ручка и шапка видны только на телефоне: там меню приезжает снизу
       шторкой, и ей нужно за что тянуть и чем представиться -->
  <div class="grabber" aria-hidden="true"><span></span></div>
  {{if .Owner}}
  <div class="menu-header">
    <span class="face" aria-hidden="true">{{firstLetter .Owner}}</span>
    <span class="owner">{{.Owner}}</span>
    <button class="icon-action" aria-label="Настройки"
            hx-get="/ui?screen=settings" hx-target="#window" hx-swap="innerHTML">{{icon "gearshape" "icon-18"}}</button>
  </div>
  {{end}}
  <div class="menu-search">
    {{icon "magnifyingglass" "search-icon"}}
    <input type="search" id="menu-search" placeholder="Поиск по разделам" autocomplete="off"
           aria-label="Поиск по разделам" oninput="sema.searchMenu(this.value)">
    <button type="button" class="clear" aria-label="Стереть" onclick="sema.searchMenu('')">
      <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M4 4l8 8M12 4l-8 8"></path></svg>
    </button>
  </div>
  <div class="menu-nothing" hidden>Ничего не нашлось. Ищется по названию раздела и по словам, которыми его обычно называют.</div>
  {{range .Items}}{{if .Pinned}}
  <button class="pinned {{.Level}}" hx-get="/ui?screen={{.Addr}}" hx-target="#window" hx-swap="innerHTML">
    {{if hasIcon .Icon}}{{icon .Icon "item-icon"}}{{end}}
    <span class="item-name">{{.Name}}</span>
    {{if .Count}}<span class="count">{{.Count}}</span>{{end}}
  </button>
  {{end}}{{end}}
  {{$group := ""}}
  {{range .Items}}
    {{if .Pinned}}{{continue}}{{end}}
    {{if .Divider}}<div class="menu-divider"></div>{{end}}
    {{if and .Group (ne .Group $group)}}{{if hasIcon .GroupIcon}}<div class="group with-icon">{{icon .GroupIcon "group-icon"}}<span>{{.Group}}</span></div>{{else}}<button type="button" class="group collapsible" data-menu-section="{{.Group}}" aria-expanded="true" onclick="sema.menuSection(this)"><span>{{.Group}}</span><svg class="group-chevron" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.6" aria-hidden="true"><path d="M3 4.5l3 3 3-3"></path></svg></button>{{end}}{{$group = .Group}}{{end}}
    {{template "menu-item" .}}
  {{end}}
  {{if .Footer}}<div class="footer">
    {{range .Footer}}
    <div class="line">
      {{if .Dot}}<span class="dot" style="background:var(--{{.Dot}})"></span>{{end}}
      <span>{{.Text}}</span>
    </div>
    {{if .Fraction}}<div class="bar"><div class="fill" style="width:{{ratio .Fraction}}"></div></div>{{end}}
    {{end}}
  </div>{{end}}
</aside>

<main id="middle">
{{if or .Panel .PanelTitle}}
{{$sections := eq .PanelKind "sections"}}
<div id="panel"{{if .PanelKind}} class="{{.PanelKind}}"{{end}}>
  <div class="panel-header">
    <div class="title">{{.PanelTitle}}</div>
    {{with .PanelAdd}}<button class="action" {{attrs .}}>+</button>{{end}}
  </div>
  {{if and $sections .Search}}
  {{/* поле поиска по настройкам — первой строкой колонки, а не в полосе
       фильтров экрана: поле одно на экран, и стоит оно там, откуда
       человек выбирает раздел. Тот же id и те же атрибуты, что у поля в
       полосе фильтров: клиент ищет его по id */}}
  <label class="search-box panel-search">
    {{icon "magnifyingglass" "search-icon"}}
    <input class="search" id="search" type="search" name="search" placeholder="{{if .SearchHint}}{{.SearchHint}}{{else}}Найти настройку{{end}}" aria-label="{{if .SearchHint}}{{.SearchHint}}{{else}}Найти настройку{{end}}" autocomplete="off" value="{{.SearchQuery}}"
           hx-get="/ui?screen={{.Addr | urlquery}}{{if .Filter}}&filter={{.Filter | urlquery}}{{end}}" hx-target="#window" hx-swap="innerHTML"
           hx-trigger="input changed delay:350ms, search">
  </label>
  {{end}}
  {{$panelGroup := ""}}
  {{range .Panel}}
    {{if eq .Kind "heading"}}<div class="panel-heading" role="heading" aria-level="2">{{.Name}}</div>{{else}}
    {{if and .Group (ne .Group $panelGroup)}}<button type="button" class="group expanded" data-group="{{if .InGroup}}{{.InGroup}}{{else}}{{.Group}}{{end}}"
      aria-expanded="true" onclick="sema.panelGroup(this)"><svg class="chevron" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M4.5 3l3 3-3 3"></path></svg>{{.Group}}</button>{{$panelGroup = .Group}}{{end}}
    <div class="panel-row {{if eq .Depth 2}}nested-2{{else if or .Nested (eq .Depth 1)}}nested-1{{end}} {{if .Kind}}{{.Kind}}{{end}}"{{if .Inside}} data-inside="{{.Inside}}"{{end}}{{if .InGroup}} data-in-group="{{.InGroup}}"{{end}}{{if eq .Kind "container"}} data-container="{{.Addr}}"{{end}}>
      {{/* контейнер сворачивается своей кнопкой, а не переходом: нажатие
           на имя открывает его экран, нажатие на шеврон прячет топики.
           Решает человек, и клиент помнит это сам */}}
      {{if eq .Kind "container"}}<button class="collapse-container {{if .Open}}expanded{{end}}" type="button"
              aria-expanded="{{if .Open}}true{{else}}false{{end}}" aria-label="Свернуть «{{.Name}}»"
              onclick="sema.container('{{.Addr}}')"><svg class="chevron" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5" aria-hidden="true"><path d="M4.5 3l3 3-3 3"></path></svg></button>{{end}}
      <button class="item {{if .Selected}}selected{{end}}"{{if .Selected}} aria-current="page"{{end}}
              hx-get="/ui?screen={{.Addr}}" hx-target="#window" hx-swap="innerHTML">
        {{if and .Level (not $sections)}}<span class="dot" style="background:var(--{{.Level}})"></span>{{end}}
        {{if eq .Kind "thread"}}<svg class="thread-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><path d="M2 3.5h9v6H5l-3 2.5z"></path><path d="M11 6.5h3v6l-2-1.5H7"></path></svg>{{end}}
        <span class="body"><span class="title">{{.Name}}</span>
        {{if .Small}}<span class="caption">{{.Small}}</span>{{end}}</span>
        {{if .Count}}<span class="counter{{if and $sections .Level}} {{.Level}}{{end}}">{{.Count}}</span>{{else if and .Level $sections}}<span class="mark {{.Level}}" role="img" aria-label="есть замечания"></span>{{end}}
      </button>
      {{with .Delete}}
      <details class="row-menu"><summary class="more" aria-label="Ещё действия"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3.5" cy="8" r="1.3"/><circle cx="8" cy="8" r="1.3"/><circle cx="12.5" cy="8" r="1.3"/></svg></summary>
        <div class="dropdown"><button class="menu-item danger" {{attrs .}}>{{.Name}}</button></div>
      </details>
      {{end}}
    </div>
    {{end}}
  {{end}}
</div>
{{end}}

{{if .OptionSets}}
{{/* Общие наборы вариантов: один раз на экран, а не в каждой строке.
     У «Моделей» девять ролей × два списка × 54 модели — это 972
     одинаковые строчки вариантов, больше трети всех узлов экрана.
     Список собирается клиентом в тот миг, когда его открыли. */}}
<script id="option-sets" type="application/json">{{optionSets .OptionSets}}</script>
{{end}}
<section id="screen" class="screen"
  data-screen="{{.Addr}}" data-limit="{{.Limit}}" data-filter="{{.Filter}}"
  data-poll="{{.PollSec}}" data-poll-wired="{{.PollSecWired}}"
  data-prefetch="{{.Prefetch}}" data-prefetch-ms="{{.PrefetchMs}}"
  {{/* снимок на устройстве: решает сервер, а читают двое — страница и
       служебный работник. Признаки пишутся всегда, «0» наравне с «1»:
       отсутствие атрибута означает разметку прежней сборки, и для неё
       запрет, а не догадка. Читать их только через getAttribute —
       кириллические data-атрибуты в dataset не попадают вовсе */}}
  data-storable="{{if .Storable}}1{{else}}0{{end}}"
  data-snapshot-differs="{{if .SnapshotDiffers}}1{{else}}0{{end}}"
  data-own-device="{{if .OwnDevice}}1{{else}}0{{end}}"
  data-dictionary="{{.Dictionary}}" data-snapshot-days="{{.SnapshotDays}}"{{if .Highlight}}
  data-highlight="{{.Highlight}}"{{end}}
  hx-get="/ui?screen={{.Addr | urlquery}}{{if .Filter}}&filter={{.Filter | urlquery}}{{end}}{{if .SearchQuery}}&search={{.SearchQuery | urlquery}}{{end}}{{if .Limit}}&limit={{.Limit}}{{end}}"
  hx-select="#feed" hx-target="#feed" hx-swap="outerHTML" hx-select-oob="#menu"
  hx-trigger="sema:refresh{{if .Composer}}, every {{.PollSec}}s{{end}}">
  <div class="header">
    <button class="action burger" data-panel="menu" aria-label="меню" onclick="sema.panel('menu')">☰</button>
    {{/* Список слева — содержимое экрана (скрипты), а на телефоне места
         для него нет: он приезжает шторкой слева, и открывает её эта
         кнопка. У колонки разделов настроек кнопки нет: на телефоне её
         не присылают вовсе, а на широком экране она стоит сама */}}
    {{if and .Panel (ne .PanelKind "sections")}}<button class="action panel-picker" data-panel="panel" type="button"
      aria-label="{{.PanelTitle}} — открыть список" aria-haspopup="dialog" aria-controls="panel" aria-expanded="false"
      onclick="sema.panel('panel')">{{.PanelTitle}} <span class="chevron-down" aria-hidden="true">⌄</span></button>{{end}}
    <div class="heading">
      {{if or .Breadcrumb .Here}}
      <nav class="crumbs" aria-label="Где мы находимся">
        {{range .Breadcrumb}}<button class="crumb" {{attrs .}}>{{.Name}}</button><svg class="crumb-chevron" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6.2 4 4 4-4 4"></path></svg>{{end}}
        {{if .Here}}<span class="here" aria-current="page">{{.Here}}</span>{{end}}
      </nav>
      {{end}}
      {{/* tabindex=-1: после смены экрана фокус переходит сюда, и скринридер
           объявляет новый экран; табом на заголовок не попасть */}}
      <h1 tabindex="-1">{{.Title}}</h1>
      {{if .Subheading}}<div class="label">{{.Subheading}}</div>{{end}}
    </div>
    {{if .Tabs}}
    {{/* вкладки экрана — своя полоса под заголовком, а не в ряду кнопок:
         кнопки шапки меняются от экрана к экрану («…» есть на «В спаме»
         и нет на «Белом списке»), и вкладки ездили вслед за ними.
         Полоса стоит в разметке до ряда кнопок: на телефоне они делят
         строку, и вкладки должны быть слева */}}
    <div class="tabs-bar">
      <div class="segments" role="tablist" aria-label="Разделы экрана">
        {{$tab := .Tab}}{{range .Tabs}}
        <a class="segment {{if eq .Addr $tab}}selected{{end}}" href="#screen={{.Addr}}" role="tab" aria-selected="{{if eq .Addr $tab}}true{{else}}false{{end}}"
           hx-get="/ui?screen={{.Addr}}" hx-target="#window" hx-swap="innerHTML" hx-push-url="false">{{.Name}}{{template "segment-count" .}}</a>
        {{end}}
      </div>
    </div>
    {{end}}
    <div class="controls">
      {{range .Selects}}
      <label class="select">{{if .Label}}<span class="select-label">{{.Label}}</span>{{end}}<select name="{{.Name}}"{{if .SpokenLabel}} aria-label="{{.SpokenLabel}}"{{end}} hx-post="/do" hx-target="#window" hx-swap="innerHTML"
              hx-vals='{"action":"{{.Action.Action}}","target":"{{.Action.Target}}"}'{{if .OptionSet}} data-option-set="{{.OptionSet}}"{{end}}>
        {{if .OptionSet}}<option value="{{.Value}}" selected>{{.Value}}</option>{{else}}{{$val := .Value}}{{range .Options}}<option value="{{.}}" {{if eq . $val}}selected{{end}}>{{.}}</option>{{end}}{{end}}
      </select></label>
      {{end}}
      {{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}
      {{if .MenuButtons}}
      <details class="row-menu">
        <summary class="more" aria-label="Ещё действия"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3.5" cy="8" r="1.3"/><circle cx="8" cy="8" r="1.3"/><circle cx="12.5" cy="8" r="1.3"/></svg></summary>
        <div class="dropdown">
          {{range .MenuButtons}}<button class="menu-item {{if eq .Kind "destructive"}}danger{{end}}" {{attrs .}}>{{.Name}}</button>{{end}}
        </div>
      </details>
      {{else if or .Buttons .Selects}}
      {{/* место под «···» занято всегда: на «В спаме» меню есть, на «Белом
           списке» нет, и «Разрешить адрес» ездила на ширину кнопки */}}
      <span class="more-slot" aria-hidden="true"></span>
      {{end}}
    </div>
  </div>

  {{if .Subtabs}}
  <div class="subtabs-bar">
    <div class="segments" role="tablist" aria-label="Разделы настройки">
      {{$subtab := .Subtab}}{{range .Subtabs}}
      <a class="segment {{if eq .Addr $subtab}}selected{{end}}" href="#screen={{.Addr}}" role="tab" aria-selected="{{if eq .Addr $subtab}}true{{else}}false{{end}}"
         hx-get="/ui?screen={{.Addr}}" hx-target="#window" hx-swap="innerHTML" hx-push-url="false">{{.Name}}{{template "segment-count" .}}</a>
      {{end}}
    </div>
  </div>
  {{end}}

  {{if or .Filters (and .Search (ne .PanelKind "sections"))}}
  <div class="filters-bar">
    {{if and .Search (ne .PanelKind "sections")}}
    <label class="search-box">
      {{icon "magnifyingglass" "search-icon"}}
      <input class="search" id="search" type="search" name="search" placeholder="{{if .SearchHint}}{{.SearchHint}}{{else}}Поиск{{end}}"{{if .SearchHint}} aria-label="{{.SearchHint}}"{{end}} value="{{.SearchQuery}}"
             hx-get="/ui?screen={{.Addr}}{{if .Filter}}&filter={{.Filter}}{{end}}" hx-target="#window" hx-swap="innerHTML"
             hx-trigger="input changed delay:350ms, search">
    </label>
    {{end}}
    {{if .Filters}}
    <div class="segments" role="tablist">
      {{$filter := .Filter}}{{range .Filters}}
      <a class="segment {{if eq .Addr $filter}}selected{{end}}" href="#screen={{$.Addr}}" hx-push-url="false" role="tab" aria-selected="{{if eq .Addr $filter}}true{{else}}false{{end}}"
         hx-get="/ui?screen={{$.Addr}}&filter={{.Addr}}{{if $.SearchQuery}}&search={{$.SearchQuery}}{{end}}" hx-target="#window" hx-swap="innerHTML">{{.Name}}</a>
      {{end}}
    </div>
    {{end}}
  </div>
  {{end}}

  {{if .Wizard}}
  <form class="form wizard" hx-post="/do" hx-target="#window" hx-swap="innerHTML">
    <h3>{{.Wizard.Title}}</h3>
    {{range .Wizard.Steps}}
    <fieldset class="step">
      <legend>{{.Title}}</legend>
      {{if .Hint}}<div class="caption">{{.Hint}}</div>{{end}}
      {{range .Fields}}{{template "field" .}}{{end}}
    </fieldset>
    {{end}}
    <div class="form-actions">
      <button class="action primary" type="submit" name="_кнопка" value="{{.Wizard.Submit.Action}}|{{.Wizard.Submit.Target}}">{{.Wizard.Submit.Name}}</button>
      <button class="action" type="submit" name="_кнопка" value="{{.Wizard.Cancel.Action}}|{{.Wizard.Cancel.Target}}" formnovalidate>{{.Wizard.Cancel.Name}}</button>
    </div>
  </form>
  {{end}}

  {{if .SaveBar}}
  {{/* уход с несохранённым: панель местная и всплывает под вкладками, у
       того места, куда человек нажал. Системного окна браузера тут нет —
       оно не умеет назвать, что именно потеряется, а «Уверены?» без
       названия правки человек закрывает не глядя */}}
  <div class="leave" id="leave" role="alert" hidden>
    <div class="gist">
      <b>Есть несохранённые изменения</b>
      <span class="what"></span>
    </div>
    <div class="right-side">
      <button type="button" class="action" data-leave="leave">Выйти без сохранения</button>
      <button type="button" class="action primary" data-leave="stay">Остаться</button>
    </div>
  </div>
  {{end}}

  <div class="feed" id="feed">
    <div class="column {{if eq .Width "read"}}read{{end}}">
      {{range .Blocks}}{{template "block" .}}{{end}}
    </div>
  </div>

  {{if .SaveBar}}
  {{/* полоса живёт вне ленты: перечитывание экрана подменяет только
       ленту, и полоса со счётом несохранённого его переживает */}}
  <div class="save-bar" id="save-bar" role="status" hidden>
    <span class="what" aria-live="polite"></span>
    <div class="right-side">
      <button type="button" class="action" data-save="revert">Отменить</button>
      <button type="button" class="action primary" data-save="save">Сохранить</button>
    </div>
  </div>
  {{end}}

  {{if .Banner}}<div class="banner">{{.Banner}}</div>{{end}}

  {{with .Composer}}
  <div class="bottom">
    {{if .To}}<div class="recipient" id="recipient" data-call-word="{{.CallWord}}" data-to="{{.To}}">уйдёт «{{.To}}»</div>{{end}}
    <form class="composer" hx-post="/do" hx-target="#window" hx-swap="innerHTML"
          {{if .Submit.Deferrable}}data-deferrable="1" {{end}}hx-vals='{"action":"{{.Submit.Action}}","target":"{{.Submit.Target}}"}'>
      {{with .Attach}}
      <label class="clip" title="Прикрепить файл">{{icon "paperclip" "attach-icon"}}
        <input type="file" name="файл" hidden{{if .Deferrable}} data-deferrable="1"{{end}}
               hx-post="/upload?screen={{$.Addr}}" hx-encoding="multipart/form-data"
               hx-target="#window" hx-swap="innerHTML" hx-trigger="change">
      </label>
      {{end}}
      <textarea id="input" name="{{.FieldName}}" rows="1" placeholder="{{.Placeholder}}"
                oninput="sema.field(this)" onkeydown="sema.input(event, this)"></textarea>
      <button class="send" type="submit" aria-label="Отправить">{{icon "paperplane" "send-icon"}}</button>
    </form>
  </div>
  {{end}}
</section>
</main>

<!-- нижняя полоса вкладок: на телефоне это оболочка, на широком экране
     её не видно — там рельс меню слева и прятать нечего -->
{{if .TabBar}}
<nav id="tabbar" aria-label="Разделы" style="--tabs:{{inc (len .TabBar)}}">
  {{/* значок и счётчик лежат в одной обёртке «tab-glyph»: счётчик
       крепится к углу значка и не двигает ни значок, ни подпись */}}
  <button class="tab" data-panel="menu" type="button" aria-label="Меню" aria-haspopup="dialog" aria-controls="menu" aria-expanded="false" onclick="sema.panel('menu')">
    <span class="tab-glyph"><svg class="tab-icon" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11"></path></svg></span>
    <span class="tab-name">Меню</span>
  </button>
  {{range .TabBar}}
  <button class="tab {{if .Selected}}selected{{end}}"
          hx-get="/ui?screen={{.Addr}}" hx-target="#window" hx-swap="innerHTML">
    <span class="tab-glyph">
      {{if hasIcon .Icon}}{{icon .Icon "tab-icon"}}{{else}}<span class="tab-icon"></span>{{end}}
      {{if .Count}}<span class="tab-count">{{.Count}}</span>{{end}}
      {{if and .Level (not .Count)}}<span class="tab-dot" style="background:var(--{{.Level}})"></span>{{end}}
    </span>
    <span class="tab-name">{{.Name}}</span>
  </button>
  {{end}}
</nav>
{{end}}

{{/* Ссылка перехода строки. Приставка адреса стоит в самом шаблоне, а
     не собирается printf: html/template проверяет протокол по первому
     двоеточию, и у готовой строки «#screen=msg:42» протоколом считается
     «#screen=msg» — ссылка молча превращалась в «#ZgotmplZ». */}}
{{define "segment-count"}}{{if .Count}}<span class="segment-count{{if .Level}} at-{{.Level}}{{end}}">{{.Count}}</span>{{end}}{{end}}

{{define "row-link"}}{{if .GoTo}}<a class="row-link" href="#screen={{.GoTo}}">{{else}}<a class="row-link" href="/api/files/{{.FileID}}" target="_blank" rel="noopener">{{end}}{{if .Name}}{{.Name}}{{else}}{{.Title}}{{end}}</a>{{end}}

{{/* Значок пункта меню — трёх видов: инициалы в кружке (разговор с
     Сёмой; на квадратной плитке — ящик), белый знак на цветной плитке
     (мессенджеры, группы), цветной контур раздела. Цвет — оттенок
     раздела, а не состояние: состояние говорит точка справа */}}
{{define "item-icon"}}{{if .Initials}}<span class="item-icon avatar{{if .Tile}} square{{end}} hue-{{.Tint}}">{{.Initials}}</span>{{else if and .Tile (hasIcon .Icon)}}<span class="item-icon tile hue-{{.Tint}}">{{icon .Icon "tile-sign"}}</span>{{else if and (hasIcon .Icon) .Tint}}{{icon .Icon (printf "item-icon hue-%s" .Tint)}}{{else if hasIcon .Icon}}{{icon .Icon "item-icon"}}{{else}}<span class="item-icon empty-icon"></span>{{end}}{{end}}

{{define "menu-item"}}
  {{if .IsNode}}
  <details class="menu-node" id="menu-{{.Key}}"{{if .Open}} open{{end}}{{if .Remember}} data-remember{{end}}{{if .Section}} data-in-section="{{.Section}}"{{end}} data-words="{{.Words}}">
    <summary class="item node {{if .Selected}}selected{{end}}">
      {{template "item-icon" .}}
      <span class="item-name">{{.Name}}</span>
      {{if .Count}}<span class="counter">{{.Count}}</span>{{end}}
      {{if .Level}}<span class="dot" style="background:var(--{{.Level}})"></span>{{end}}
      {{/* по спецификации нажатие кнопки внутри summary узел не переключает, и в
           Chrome это проверено тестом; preventDefault — страховка для старых
           WebKit (Safari), где было иначе, а проверить его здесь нечем */}}
      {{with .Add}}<button type="button" class="item-plus" aria-label="{{.Name}}" title="{{.Name}}" onclick="event.preventDefault()" {{attrs .}}>+</button>{{end}}
      <span class="chevron" aria-hidden="true">›</span>
    </summary>
    <div class="well">
      {{if .Addr}}<button class="item self {{if .Selected}}selected{{end}}"
              hx-get="/ui?screen={{.Addr}}" hx-target="#window" hx-swap="innerHTML">
        <span class="item-name">Обзор</span>
      </button>{{end}}
      {{range .Children}}{{template "menu-item" .}}{{end}}
    </div>
  </details>
  {{else}}
  <button class="item {{if .Selected}}selected{{end}}" data-words="{{.Words}}"{{if .Section}} data-in-section="{{.Section}}"{{end}}
          hx-get="/ui?screen={{.Addr}}" hx-target="#window" hx-swap="innerHTML">
    {{template "item-icon" .}}
    <span class="item-name">{{.Name}}</span>
    {{if .Count}}<span class="counter">{{.Count}}</span>{{end}}
    {{if .Level}}<span class="dot" style="background:var(--{{.Level}})"></span>{{end}}
  </button>
  {{with .Add}}<button type="button" class="item-plus standalone" aria-label="{{.Name}}" title="{{.Name}}" {{attrs .}}>+</button>{{end}}
  {{end}}
{{end}}

{{define "block"}}
  {{if eq .Kind "заголовок"}}<h2 class="subheading">{{.Text}}</h2>{{end}}
  {{if eq .Kind "группа"}}<div class="section-heading">{{.Text}}</div>{{end}}
  {{if eq .Kind "черта"}}<hr class="divider">{{end}}
  {{/* «callout» — подсвеченное пояснение над настройками: жирное начало
       (Component.Title) и обычный текст следом */}}
  {{if eq .Kind "текст"}}{{if eq .Style "callout"}}<div class="callout">{{if .Title}}<b>{{.Title}}</b> {{end}}{{.Text}}</div>{{else}}<div class="{{if eq .Style "secondary"}}caption{{else if eq .Style "day"}}feed-day{{else if eq .Style "error"}}error{{else}}paragraph{{end}}">{{.Text}}</div>{{end}}{{end}}

  {{if eq .Kind "граф"}}
  {{template "graph" .Graph}}
  <details class="expansion-wrap"><summary>Списком</summary>
    {{range .Children}}{{template "block" .}}{{end}}
  </details>
  {{end}}

  {{if eq .Kind "пусто"}}
  <div class="empty">{{if .Avatar}}<div class="icon" aria-hidden="true">{{.Avatar}}</div>{{end}}<div class="title">{{.Name}}</div>{{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}{{range $i, $btn := .Buttons}}{{if eq $i 0}}<button class="action {{$btn.Kind}}" {{attrs $btn}}>{{$btn.Name}}</button>{{end}}{{end}}</div>
  {{end}}

  {{if eq .Kind "кнопки"}}
  <div class="right-side" style="justify-content:flex-start">
    {{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "карточки"}}
  <div class="section">
    {{if .Title}}<div class="section-heading">{{.Title}}</div>{{end}}
    <div class="cards">
      {{range .Children}}{{template "block" .}}{{end}}
    </div>
  </div>
  {{end}}

  {{if or (eq .Kind "карточка") (eq .Kind "показатель")}}
  <div class="card{{if .Level}} level-{{.Level}}{{end}}">
    {{if .Level}}<span class="level-bar"></span>{{end}}
    {{if .Title}}<div class="label">{{.Title}}</div>{{end}}
    <div class="value-large{{if eq .Style "прозой"}} прозой{{end}}">{{.Text}}{{if .Unit}}<span class="unit">{{.Unit}}</span>{{end}}</div>
    {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
    {{if .Pills}}<div class="right-side" style="justify-content:flex-start">{{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}</div>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "матрица"}}
  <div class="section">
    {{if .Title}}<div class="section-heading">{{.Title}}</div>{{end}}
    <div class="matrix-scroll">
      {{/* на телефоне шапки колонок нет, а строка раскладывается названием и
           рядом чипов прав: подпись колонки печатается внутри каждой клетки
           (cell-label) — рисует её только узкий экран. Клетка «na» на телефоне
           не рисуется вовсе: права, которого не бывает, не предлагают */}}
      {{$cols := .Columns}}
      <table class="matrix{{if eq .Style "rights"}} rights{{end}}">
        <thead>
          <tr><th></th>{{range .Columns}}<th>{{.}}</th>{{end}}{{if .Controls}}<th></th>{{end}}</tr>
        </thead>
        <tbody>
        {{$controls := .Controls}}
        {{range .Children}}
          <tr{{if .ID}} id="mx-{{.ID}}"{{end}} class="matrix-row{{if .Level}} {{.Level}}{{end}}{{if eq .Style "nested"}} nested{{end}}">
            <td class="who">
              <span class="title">{{.Name}}{{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}</span>
              {{if .Caption}}<small>{{.Caption}}</small>{{end}}
            </td>
            {{range $i, $cell := .Cells}}
            <td class="cell{{if eq $cell.State "na"}} na{{end}}">
              {{if eq $cell.State "na"}}<span class="cell-na" title="{{$cell.Tooltip}}" role="img" aria-label="{{$cell.Tooltip}}">–</span>
              {{else}}<button class="cell-button {{$cell.State}}{{if $cell.Inherited}} inherited{{end}}" title="{{$cell.Tooltip}}" {{attrs $cell.Button}}>{{if eq $cell.State "on"}}<span class="cell-mark">✓</span>{{else if eq $cell.State "out"}}<span class="cell-mark">↗</span>{{end}}<span class="cell-label">{{at $cols $i}}</span></button>
              {{end}}
            </td>
            {{end}}
            {{if $controls}}
            <td class="cell controls">{{if or .Buttons .Menu}}<div class="right-side">{{template "row-controls" .}}</div>{{end}}</td>
            {{end}}
          </tr>
        {{end}}
        </tbody>
      </table>
    </div>
    {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "таблица"}}
  <div class="section">
    {{if .Title}}<div class="section-heading">{{.Title}}</div>{{end}}
    <div class="table-scroll">
      <table class="table">
        <thead><tr>{{$rightAligned := .RightAligned}}{{range $i, $col := .Columns}}<th{{if $rightAligned}}{{if index $rightAligned $i}} class="right-aligned"{{end}}{{end}}>{{$col}}</th>{{end}}</tr></thead>
        <tbody>
        {{/* подписи колонок едут и в ячейки (data-label): на телефоне шапки
             нет, строка раскладывается карточкой, и подпись печатается перед
             значением. Первая колонка — название строки, ячейки идут со второй */}}
        {{$cols := .Columns}}
        {{range .Children}}
          <tr{{if .Level}} class="table-row {{.Level}}"{{end}}{{if .ID}} data-hl="r:{{.ID}}"{{end}}>
            <td class="who">
              <span class="title">{{.Name}}{{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}</span>
              {{if .Caption}}<small>{{.Caption}}</small>{{end}}
            </td>
            {{range $i, $cell := .Cells}}
            <td class="table-cell{{if $cell.Mono}} mono{{end}}{{if $cell.Level}} {{$cell.Level}}{{end}}{{if $cell.Select}} has-select{{end}}"{{if at $cols (inc $i)}} data-label="{{at $cols (inc $i)}}"{{end}}{{if $cell.Tooltip}} title="{{$cell.Tooltip}}"{{end}}>
              {{if $cell.Select}}{{template "select" $cell.Select}}{{else if $cell.Button.Action}}<button class="action link" {{attrs $cell.Button}}>{{if $cell.Text}}{{$cell.Text}}{{else}}{{$cell.Button.Name}}{{end}}</button>{{else}}{{$cell.Text}}{{end}}
            </td>
            {{end}}
            {{if or .Toggle .Buttons .Selects .Menu}}
            <td class="table-cell controls">
              <div class="right-side">
                {{template "row-controls" .}}
              </div>
            </td>
            {{end}}
          </tr>
        {{end}}
        </tbody>
      </table>
    </div>
    {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "капсулы-ряд"}}
  <div class="pill-row">
    {{if .Title}}<div class="section-heading">{{.Title}}</div>{{end}}
    <div class="chips-row">
      {{range .Pills}}<span class="pill set {{.Level}}">{{.Text}}</span>{{end}}
      {{if .More}}<span class="pill tail">{{.More}}</span>{{end}}
      {{range .Buttons}}<button class="pill tail pressable" {{attrs .}}>{{.Name}}</button>{{end}}
    </div>
    {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "шаг"}}
  <div class="wizard-step {{if .Style}}{{.Style}}{{end}}"{{if .ID}} id="wz-{{.ID}}"{{end}}>
    <div class="step-header">
      <span class="number" aria-hidden="true">{{if eq .Style "done"}}<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M3.2 8.4 6.3 11.4 12.8 4.9"></path></svg>{{else}}{{.Text}}{{end}}</span>
      <span class="title">{{.Name}}</span>
      {{if .Caption}}<span class="result">{{.Caption}}</span>{{end}}
      <div class="right-side">
        {{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}
        {{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}
      </div>
    </div>
    {{if .Children}}<div class="step-body">{{range .Children}}{{template "block" .}}{{end}}</div>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "путь"}}
  <nav class="crumbs in-feed" aria-label="Где мы находимся">
    {{range .Buttons}}<button class="crumb" {{attrs .}}>{{.Name}}</button><svg class="crumb-chevron" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m6.2 4 4 4-4 4"></path></svg>{{end}}
    {{if .Title}}<span class="here" aria-current="page">{{.Title}}</span>{{end}}
  </nav>
  {{end}}

  {{if eq .Kind "полка"}}
  <div class="shelf">
    <div class="shelf-header">
      <span class="what">{{.Title}}</span>
      {{if .Meta}}<span class="count">{{.Meta}}</span>{{end}}
      {{if .Buttons}}<span class="shelf-actions">{{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}</span>{{end}}
    </div>
    {{range .Children}}
    {{if eq .Kind "группа-строк"}}{{template "block" .}}{{else}}
    {{/* та же беда, что у обычной строки: роль ссылки на родителе кнопок
         — неправда для клавиатуры и для чтения вслух. Ссылка живёт на
         названии; роль остаётся, только когда называть нечего */}}
    {{$shelfLink := and .GoTo (ne .Name "")}}
    <div class="shelf-row row{{if .GoTo}} clickable{{end}}"{{if and .GoTo (not $shelfLink)}} tabindex="0" role="link"{{end}}{{if .GoTo}} data-goto="{{.GoTo}}"{{end}}>
      <div class="body">
        <div class="title">{{if $shelfLink}}<a class="row-link" href="#screen={{.GoTo}}">{{.Name}}</a>{{else}}{{.Name}}{{end}}</div>
        {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
      </div>
      <div class="right-side">
        {{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}
        {{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}
        {{if .Menu}}
        <details class="row-menu">
          <summary class="more" aria-label="Ещё действия"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3.5" cy="8" r="1.3"/><circle cx="8" cy="8" r="1.3"/><circle cx="12.5" cy="8" r="1.3"/></svg></summary>
          <div class="dropdown">
            {{range .Menu}}<button class="menu-item {{if eq .Kind "destructive"}}danger{{end}}" {{attrs .}}>{{.Name}}</button>{{end}}
          </div>
        </details>
        {{end}}
      </div>
    </div>
    {{end}}
    {{end}}
  </div>
  {{end}}

  {{if eq .Kind "группа-строк"}}
  <details class="row-group"{{if .ID}} id="grp-{{.ID}}"{{end}}>
    <summary>
      <span class="title">{{.Name}}</span>
      {{if .Caption}}<span class="caption">{{.Caption}}</span>{{end}}
      <span class="count">×{{len .Children}}</span>
    </summary>
    <div class="inside">{{range .Children}}{{template "block" .}}{{end}}</div>
  </details>
  {{end}}

  {{if eq .Kind "тревога"}}
  <div class="alert-box">
    <div class="body">
      <div class="title">{{.Name}}</div>
      {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
    </div>
    <div class="right-side">{{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}</div>
  </div>
  {{end}}

  {{if eq .Kind "числа"}}
  <div class="figures">
    {{range .Children}}
    <div class="figure">
      <b>{{.Text}}{{if .Unit}}<span class="unit">{{.Unit}}</span>{{end}}</b>
      <span>{{.Title}}</span>
    </div>
    {{end}}
  </div>
  {{end}}

  {{if eq .Kind "раздел"}}
  <div class="section {{if eq .Style "thread"}}thread-quote{{end}}{{if eq .Style "sticky"}}pinned-bar{{end}}{{if eq .Style "danger"}} danger{{end}}{{if eq .Style "stack"}} stack{{end}}"{{if .ID}} id="sec-{{.ID}}" data-hl="s:{{.ID}}"{{end}}>
    {{if .Fragment}}
      {{/* содержимое приедет при раскрытии: тело пустое, поле поиска и
           кнопка «ещё» просят у сервера часть, а не весь экран */}}
      <details class="fold fragment{{if eq .Style "fine"}} fine{{end}}" {{if .Open}}open{{end}}{{if .ID}} id="fold-{{.ID}}"{{end}} data-fragment="{{.Fragment}}">
        {{/* «Тонкая настройка»: редкое экрана одной свёрнутой строкой. Слева
             сколько параметров и о чём, справа «всё по умолчанию» или «K
             изменены»: человек видит, стоит ли раскрывать */}}
        <summary class="section-heading">{{.Title}}{{if eq .Style "fine"}}{{if .Caption}}<span class="fine-note">{{.Caption}}</span>{{end}}{{if .Meta}}<span class="fine-state{{if eq .Level "info"}} changed{{end}}">{{.Meta}}</span>{{end}}{{end}}</summary>
        <div class="section-card">
          {{if .FragmentSearch}}<label class="search-box fragment-search">{{icon "magnifyingglass" "search-icon"}}<input class="search" type="search" placeholder="{{.FragmentSearch}}" aria-label="{{.FragmentSearch}}" autocomplete="off"></label>{{end}}
          {{/* id нужен htmx: подмену он делает только по селектору, элемент
               целью не принимает — с элементом тело молча оставалось пустым */}}
          <div class="fragment-body" id="body-{{.Fragment}}" aria-live="polite"><div class="caption fragment-loading">Загружаю…</div></div>
        </div>
      </details>
    {{else if .Collapsed}}
      <details class="fold" {{if .Open}}open{{end}}{{if .ID}} id="fold-{{.ID}}"{{end}}>
        <summary class="section-heading">{{.Title}}</summary>
        {{if .Children}}<div class="{{if eq .Style "thread"}}rail{{else}}section-card{{end}}">
          {{range .Children}}{{template "block" .}}{{end}}
        </div>{{end}}
      </details>
    {{else}}
      {{if or .Title .Buttons}}
      <div class="section-header">
        {{if .Title}}<div class="section-heading">{{.Title}}</div>{{end}}
        {{if .Buttons}}<div class="right-side">{{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}</div>{{end}}
      </div>
      {{end}}
      {{if .Children}}<div class="{{if eq .Style "thread"}}rail{{else}}section-card{{end}}">
        {{range .Children}}{{template "block" .}}{{end}}
      </div>{{end}}
    {{end}}
  </div>
  {{end}}

  {{/* вид «ожидание», а класс «думает»: класс «ожидание» уже занят
       заглушкой «Подключаюсь к Сёме…» в оболочке */}}
  {{if eq .Kind "ожидание"}}
  <div class="thinking"{{if .ID}} id="wait-{{.ID}}"{{end}} role="status" aria-live="polite">
    <span class="dots" aria-hidden="true"><i></i><i></i><i></i></span>
    <span class="what">{{.Title}}</span>
    {{if .Meta}}<span class="by-whom">{{.Meta}}</span>{{end}}
    {{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "карточка-дела"}}
  <div class="matter{{if .Level}} level-{{.Level}}{{end}}"{{if .ID}} id="matter-{{.ID}}"{{end}}>
    {{if .Level}}<span class="level-bar"></span>{{end}}
    <div class="matter-header">
      {{if .RowIcon}}<span class="matter-icon {{.Level}}">{{icon .RowIcon "icon-16"}}</span>{{end}}
      <span class="matter-name">
        <b>{{.Title}}</b>
        {{if .Meta}}<span class="matter-meta">{{.Meta}}</span>{{end}}
      </span>
      <span class="right-side">
        {{if .CountdownTo}}<span class="countdown-large" data-until="{{.CountdownTo}}">…</span>{{end}}
        {{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}
      </span>
    </div>
    {{if or .Text .Output .Links .Children .Files .Caption .Name}}
    <div class="matter-body">
      {{if .Text}}<div class="matter-prose">{{.Text}}</div>{{end}}
      {{if .Output}}
        {{if eq .Style "разница"}}<div class="matter-diff">{{range diff .Output}}<div class="{{.Sign}}">{{.Text}}</div>{{end}}</div>
        {{else}}<pre class="matter-code">{{.Output}}</pre>{{end}}
      {{end}}
      {{if .Links}}<div class="matter-links">{{range .Links}}<button class="matter-link" {{attrs .}}>{{.Name}}</button>{{end}}</div>{{end}}
      {{range .Children}}{{template "block" .}}{{end}}
      {{if .Files}}<div class="attachments">{{template "files" .Files}}</div>{{end}}
      {{if .Caption}}<div class="matter-quiet">{{.Caption}}</div>{{end}}
      {{if .Name}}<div class="matter-caution"><span aria-hidden="true">▲</span><span>{{.Name}}</span></div>{{end}}
    </div>
    {{end}}
    {{if or .Buttons .Menu}}
    <div class="matter-actions">
      {{range .Buttons}}{{if ne .Kind "destructive"}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}{{end}}
      {{if hasDestructive .Buttons}}
      <span class="spacer"></span>
      {{range .Buttons}}{{if eq .Kind "destructive"}}<button class="action destructive" {{attrs .}}>{{.Name}}</button>{{end}}{{end}}
      {{end}}
      {{if .Menu}}
      <details class="row-menu">
        <summary class="more" aria-label="Ещё действия"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3.5" cy="8" r="1.3"/><circle cx="8" cy="8" r="1.3"/><circle cx="12.5" cy="8" r="1.3"/></svg></summary>
        <div class="dropdown">
          {{range .Menu}}<button class="menu-item {{if eq .Kind "destructive"}}danger{{end}}" {{attrs .}}>{{.Name}}</button>{{end}}
        </div>
      </details>
      {{end}}
    </div>
    {{end}}
  </div>
  {{end}}

  {{if eq .Kind "цепочка"}}
  <div class="section">
    <div class="section-header">
      {{if .Title}}<div class="section-heading">{{.Title}}</div>{{end}}
      {{if .Meta}}<span class="chain-count">{{.Meta}}</span>{{end}}
    </div>
    <div class="chain">
      {{if .Cells}}
      <div class="scale">
        {{range .Cells}}<span class="notch{{if eq .State "on"}} current{{end}}" title="{{.Tooltip}}"></span>{{end}}
        {{if .Caption}}<span class="caption">{{.Caption}}</span>{{end}}
      </div>
      {{end}}
      {{if .Children}}<div class="chain-mails">{{range .Children}}{{template "block" .}}{{end}}</div>{{end}}
      {{if .Buttons}}<div class="right-side" style="justify-content:flex-start">
        {{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}
      </div>{{end}}
    </div>
  </div>
  {{end}}

  {{if eq .Kind "класс"}}
  <div class="mail-class">
    {{if .RowIcon}}<span class="matter-icon">{{icon .RowIcon "icon-16"}}</span>{{end}}
    <div class="body">
      <div class="title">{{.Title}}</div>
      {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
    </div>
    <div class="segments" role="group" aria-label="Класс чувствительности">
      {{$val := .Text}}
      {{range .Buttons}}<button class="segment{{if eq .Name $val}} selected{{end}}"{{if eq .Name $val}} aria-pressed="true"{{end}} {{attrs .}}>{{.Name}}</button>{{end}}
    </div>
  </div>
  {{end}}

  {{if eq .Kind "образцы"}}
  {{/* правила сервера тремя колонками: «Разрешить», «Спросить», «Запретить».
       В колонке — образцы капсулами с «×» и поле «добавить»; образец,
       по которому решена проверяемая команда, подсвечен. Колонка —
       ребёнок этого блока: её заголовок, пояснение, уровень и образцы. */}}
  <div class="pattern-cols"{{if .ID}} id="pat-{{.ID}}"{{end}}>
    {{range .Children}}{{$col := .}}
    <div class="pattern-col {{.Level}}"{{if .ID}} id="pat-{{.ID}}"{{end}}>
      <h3><span>{{.Title}}</span><span class="pill {{.Level}}">{{len .Children}}</span></h3>
      {{if .Caption}}<p class="explain">{{.Caption}}</p>{{end}}
      <div class="pats">
        {{range .Children}}{{$chip := .}}<span class="pat{{if eq .Style "hit"}} hit{{end}}"><span>{{.Text}}</span>{{range .Buttons}}<button type="button" class="pat-x" aria-label="убрать образец {{$chip.Text}}" {{attrs .}}>×</button>{{end}}</span>{{end}}
        {{if not .Children}}<span class="hint">пусто</span>{{end}}
      </div>
      {{range .Buttons}}
      <form class="pat-add" hx-post="/do" hx-target="#window" hx-swap="innerHTML">
        <input type="text" name="value" placeholder="образец команды" aria-label="Новый образец: {{$col.Title}}" autocomplete="off" spellcheck="false">
        <button class="action" type="submit" name="_кнопка" value="{{.Action}}|{{.Target}}">{{.Name}}</button>
      </form>
      {{end}}
    </div>
    {{end}}
  </div>
  {{end}}

  {{if eq .Kind "переключатель"}}
  <div class="switcher">
    {{if .Title}}<div class="section-heading">{{.Title}}</div>{{end}}
    <div class="entities">
      {{range .Children}}
      <button class="entity{{if eq .Style "current"}} selected{{end}}"{{if eq .Style "current"}} aria-current="true"{{end}}
              {{if .GoTo}}hx-get="/ui?screen={{.GoTo}}" hx-target="#window" hx-swap="innerHTML"{{end}}>
        <span class="top">
          {{if .Level}}<span class="dot" style="background:var(--{{.Level}})"></span>{{end}}
          <span class="name">{{.Name}}</span>
          {{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}
        </span>
        {{if .Caption}}<span class="bottom">{{.Caption}}</span>{{end}}
      </button>
      {{end}}
    </div>
  </div>
  {{end}}

  {{if eq .Kind "пузырь"}}
  <div class="turn">
    {{if .Title}}<div class="who {{if or (eq .Role "свой") (eq .Role "ушло")}}right{{end}}">{{.Title}}</div>{{end}}
    <div class="bubble {{.Role}}"><div class="text">{{.Text}}</div>{{if .Menu}}<details class="row-menu bubble-menu">
      <summary class="more" aria-label="Ещё действия"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3.5" cy="8" r="1.3"/><circle cx="8" cy="8" r="1.3"/><circle cx="12.5" cy="8" r="1.3"/></svg></summary>
      <div class="dropdown">{{range .Menu}}<button class="menu-item" {{attrs .}}>{{.Name}}</button>{{end}}</div>
    </details>{{end}}</div>
    {{if .Files}}<div class="attachments {{.Role}}">{{template "files" .Files}}</div>{{end}}
    {{if .Meta}}<div class="meta {{if or (eq .Role "свой") (eq .Role "ушло")}}right{{end}}">{{.Meta}}</div>{{end}}
  </div>
  {{end}}

  {{if eq .Kind "форма"}}
  {{if .Deferred}}
  {{/* форму сохраняет нижняя полоса экрана: своей кнопки нет, а действие
       лежит скрытым полем — полоса отправляет ровно то же, что отправила
       бы кнопка. hx-post тут не нужен: полоса шлёт форму сама и ждёт «да
       или нет», а не экран целиком, иначе сохранение первой формы стёрло
       бы несохранённое в остальных */}}
  <form class="form deferred{{if eq .Style "rows"}} rows{{end}}" data-form="{{.ID}}">
    {{if .Title}}<h3>{{.Title}}</h3>{{end}}
    {{template "form-fields" .}}
    {{range $i, $btn := .Buttons}}{{if eq $i 0}}<input type="hidden" name="_кнопка" value="{{$btn.Action}}|{{$btn.Target}}">{{end}}{{end}}
    {{with toggleNames .Fields}}<input type="hidden" name="toggles" value="{{.}}">{{end}}
    <div class="not-saved" hidden></div>
  </form>
  {{else}}
  <form class="form{{if eq .Style "rows"}} rows{{end}}" hx-post="/do" hx-target="#window" hx-swap="innerHTML">
    {{if .Title}}<h3>{{.Title}}</h3>{{end}}
    {{template "form-fields" .}}
    {{/* снятый флажок браузер не шлёт вовсе: список тумблеров формы
         говорит серверу, какие из них выключены, а какие не при чём */}}
    {{with toggleNames .Fields}}<input type="hidden" name="toggles" value="{{.}}">{{end}}
    {{/* ряд кнопок — свой класс, а не right-side строки списка: у того нет
         отступа сверху, и кнопка липла к последнему полю */}}
    <div class="form-actions">
      {{range .Buttons}}<button class="action {{.Kind}}" type="submit" name="_кнопка" value="{{.Action}}|{{.Target}}">{{.Name}}</button>{{end}}
    </div>
  </form>
  {{end}}
  {{end}}

  {{if eq .Kind "строка"}}
  {{/* переход у строки живёт ссылкой на её названии, а не ролью «link» у
       всей строки: внутри строки стоят галочка выбора и кнопки, и роль
       ссылки на их общем родителе — неправда для клавиатуры и для чтения
       вслух (строка объявлялась одной ссылкой, а кнопки внутри неё
       терялись). Мышью по-прежнему кликается вся строка. Роль остаётся
       только там, где называть нечего и ссылке не на чем держаться. */}}
  {{$hasLink := and (or .GoTo .FileID) (or (ne .Name "") (ne .Title ""))}}
  <div class="row-wrap"{{if .ID}} data-hl="r:{{.ID}}"{{end}}>
    <div class="row {{if .Style}}{{.Style}}{{end}}{{if .Wide}} wide{{end}}{{if .Tone}} tone-{{.Tone}}{{end}}{{if or .GoTo .FileID}} clickable{{end}}"{{if and (or .GoTo .FileID) (not $hasLink)}} tabindex="0" role="link"{{end}}{{if .GoTo}} data-goto="{{.GoTo}}"{{end}}{{if .FileID}} data-file="{{.FileID}}"{{end}}>
      {{if .Level}}<span class="dot" style="background:var(--{{.Level}})"></span>{{end}}
      {{if .Toggle}}
        {{range .Buttons}}{{if eq .Name "toggle"}}
        <button class="switch {{if eq $.Toggle "on"}}on{{end}}" role="switch" aria-checked="{{if eq $.Toggle "on"}}true{{else}}false{{end}}" aria-label="{{if $.Name}}{{$.Name}}{{else}}переключить{{end}}" {{attrs .}}></button>
        {{end}}{{end}}
      {{end}}
      {{if .Selectable}}<label class="row-check"><input type="checkbox" name="выбрано" value="{{.ID}}" aria-label="Отметить"><span aria-hidden="true"></span></label>{{end}}
      {{if .RowIcon}}<span class="row-icon"{{if .AvatarColor}} style="color:{{.AvatarColor}}"{{end}}>{{icon .RowIcon "icon-16"}}</span>{{end}}
      {{if .Avatar}}<span class="avatar {{.AvatarKind}}"{{if .AvatarColor}} style="background:{{.AvatarColor}}"{{end}}>{{.Avatar}}</span>{{end}}
      <div class="body">
        {{$linkInTitle := and $hasLink (eq .Name "")}}
        {{if and .Title (not .MetaInline)}}<div class="above">{{if $linkInTitle}}{{template "row-link" .}}{{else}}{{.Title}}{{end}}</div>{{end}}
        {{if or .Name .MetaInline}}
        <div class="name-line">
          {{if .Name}}<span class="title">{{if $hasLink}}{{template "row-link" .}}{{else}}{{.Name}}{{end}}</span>{{end}}
          {{if and .MetaInline .Meta}}<span class="meta-inline">{{.Meta}}</span>{{end}}
        </div>
        {{end}}
        {{if and .Title .MetaInline}}<div class="main">{{if $linkInTitle}}{{template "row-link" .}}{{else}}{{.Title}}{{end}}</div>{{end}}
        {{if .Caption}}<div class="caption">{{.Caption}}</div>{{end}}
        {{if .Facts}}<div class="facts">{{range .Facts}}<span><span class="label">{{.Label}}</span><span class="value">{{.Value}}</span></span>{{end}}</div>{{end}}
        {{if and .Meta (not .MetaInline)}}<div class="meta">{{.Meta}}</div>{{end}}
        {{if .Fraction}}<div class="gauge"><div class="fill" style="width:{{percent .Fraction}}"></div></div>{{end}}
      </div>
      <div class="right-side">
        {{if .Sparkline}}<svg class="sparkline" viewBox="0 0 160 28" preserveAspectRatio="none" aria-hidden="true"><polyline points="{{.Sparkline}}"/></svg>{{end}}
        {{if .CountdownTo}}<span class="pill info countdown" data-until="{{.CountdownTo}}">…</span>{{end}}
        {{range .Pills}}<span class="pill {{.Level}}">{{.Text}}</span>{{end}}
        {{range .Selects}}
        <label class="select">{{if .Label}}<span class="select-label">{{.Label}}</span>{{end}}<select name="{{.Name}}"{{if .SpokenLabel}} aria-label="{{.SpokenLabel}}"{{end}} hx-post="/do" hx-target="#window" hx-swap="innerHTML"
                hx-vals='{"action":"{{.Action.Action}}","target":"{{.Action.Target}}"}'{{if .OptionSet}} data-option-set="{{.OptionSet}}"{{end}}>
          {{if .OptionSet}}<option value="{{.Value}}" selected>{{.Value}}</option>{{else}}{{$val := .Value}}{{range .Options}}<option value="{{.}}" {{if eq . $val}}selected{{end}}>{{.}}</option>{{end}}{{end}}
        </select></label>
        {{end}}
        {{range .Buttons}}{{if ne .Name "toggle"}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}{{end}}
        {{if .Menu}}
        <details class="row-menu">
          <summary class="more" aria-label="Ещё действия"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3.5" cy="8" r="1.3"/><circle cx="8" cy="8" r="1.3"/><circle cx="12.5" cy="8" r="1.3"/></svg></summary>
          <div class="dropdown">
            {{range .Menu}}<button class="menu-item {{if eq .Kind "destructive"}}danger{{end}}" {{attrs .}}>{{.Name}}</button>{{end}}
          </div>
        </details>
        {{end}}
      </div>
    </div>
    {{if or .Output .Props .Commands .Files .Children .FrameURL}}
    <details class="expansion-wrap" {{if .Open}}open{{end}}{{if .ID}} id="exp-{{.ID}}"{{end}}>
      <summary class="details-more">{{if .Expand}}{{.Expand}}{{else}}Подробнее{{end}}</summary>
      <div class="expansion">
        {{if .Props}}<div class="facts">{{range .Props}}<span><span class="label">{{.Label}}</span><span class="value">{{.Value}}</span></span>{{end}}</div>{{end}}
        {{/* картинка — картинкой, а не рамкой: файл отдаётся с запретом
             встраивания, и в рамке человек видел «сайт не позволяет
             установить соединение» вместо своей картинки */}}
        {{if .ImageURL}}<a class="row-image" href="{{.ImageURL}}" target="_blank" rel="noopener"><img src="{{.ImageURL}}" alt="{{.Title}}" loading="lazy"></a>{{end}}
        {{if .FrameURL}}
        {{/* письмо как есть: чужая разметка живёт в своём документе и в
             песочнице, а не в адресе Сёмы. Текстовый вариант лежит рядом
             и показывается тем же переключателем */}}
        <div class="mail-view" data-url="{{.FrameURL}}">
          <div class="view-switch" role="group" aria-label="Как показать письмо">
            <button type="button" class="view selected" data-view="as-mail">Как в письме</button>
            {{if .Output}}<button type="button" class="view" data-view="as-text">Текстом</button>{{end}}
            {{if .Translation}}<button type="button" class="view translation" data-translation="{{.Translation}}">Перевод {{.Translation}}</button>{{end}}
            <button type="button" class="view grow">Развернуть</button>
            {{if .BlockedImages}}<button type="button" class="view images" data-images="{{.BlockedImages}}">Показать картинки · {{.BlockedImages}}</button>{{end}}
          </div>
          {{/* адрес лежит в data-src: рамка, приехавшая подменой htmx,
               загрузку по src не начинала — письмо было пустым белым
               полем, пока src не переустановят (14 сентября 2026).
               Ставит его клиент, когда рамка уже в документе */}}
          <iframe class="mail-frame" data-src="{{.FrameURL}}" sandbox="allow-popups allow-popups-to-escape-sandbox" referrerpolicy="no-referrer" title="Письмо как есть"></iframe>
          {{if .Output}}<pre class="output mail-as-text" hidden>{{.Output}}</pre>{{end}}
          {{if .BlockedImages}}<div class="caption">Внешние картинки не загружены: пока браузер за ними не сходил, отправитель не знает, что письмо открыли.</div>{{end}}
        </div>
        {{else}}{{if .Output}}<pre class="output">{{.Output}}</pre>{{end}}{{end}}
        {{range .Commands}}
        <div class="command">
          <code>{{.Text}}</code>
          {{if .Note}}<div class="caption">{{.Note}}</div>{{end}}
          <div class="right-side" style="justify-content:flex-start">
            {{range .Buttons}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}
          </div>
        </div>
        {{end}}
        {{if .Files}}<div class="attachments">{{template "files" .Files}}</div>{{end}}
        {{range .Children}}{{template "block" .}}{{end}}
      </div>
    </details>
    {{end}}
  </div>
  {{end}}
{{end}}

{{/* управление строки: тумблер, списки, кнопки, меню. Вынесено, чтобы
     строка таблицы управлялась ровно тем же, чем строка списка */}}
{{define "row-controls"}}
  {{$row := .}}
  {{if .Toggle}}
    {{range .Buttons}}{{if eq .Name "toggle"}}
    <button class="switch {{if eq $row.Toggle "on"}}on{{end}}" role="switch" aria-checked="{{if eq $row.Toggle "on"}}true{{else}}false{{end}}" aria-label="{{if $row.Name}}{{$row.Name}}{{else}}переключить{{end}}" {{attrs .}}></button>
    {{end}}{{end}}
  {{end}}
  {{range .Selects}}{{template "select" .}}{{end}}
  {{range .Buttons}}{{if ne .Name "toggle"}}<button class="action {{.Kind}}" {{attrs .}}>{{.Name}}</button>{{end}}{{end}}
  {{if .Menu}}
  <details class="row-menu">
    <summary class="more" aria-label="Ещё действия"><svg viewBox="0 0 16 16" aria-hidden="true"><circle cx="3.5" cy="8" r="1.3"/><circle cx="8" cy="8" r="1.3"/><circle cx="12.5" cy="8" r="1.3"/></svg></summary>
    <div class="dropdown">
      {{range .Menu}}<button class="menu-item {{if eq .Kind "destructive"}}danger{{end}}" {{attrs .}}>{{.Name}}</button>{{end}}
    </div>
  </details>
  {{end}}
{{end}}

{{/* Выпадающий список, который применяется сразу: в строке, в шапке и в
     ячейке таблицы. Общий набор вариантов (OptionSet) собирает клиент в тот
     миг, когда список открыли, — в самой разметке остаётся одно значение */}}
{{define "select"}}<label class="select">{{if .Label}}<span class="select-label">{{.Label}}</span>{{end}}<select name="{{.Name}}"{{if .SpokenLabel}} aria-label="{{.SpokenLabel}}"{{end}} hx-post="/do" hx-target="#window" hx-swap="innerHTML"
          hx-vals='{"action":"{{.Action.Action}}","target":"{{.Action.Target}}"}'{{if .OptionSet}} data-option-set="{{.OptionSet}}"{{end}}>
    {{if .OptionSet}}<option value="{{.Value}}" selected>{{.Value}}</option>{{else}}{{$val := .Value}}{{range .Options}}<option value="{{.}}" {{if eq . $val}}selected{{end}}>{{.}}</option>{{end}}{{end}}
  </select></label>{{end}}

{{define "graph"}}
<figure class="graph" role="img" aria-label="Граф памяти: {{len .Nodes}} узлов и {{len .Edges}} связей">
  <svg viewBox="0 0 {{.Width}} {{.Height}}" preserveAspectRatio="xMidYMid meet">
    {{range .Edges}}<line x1="{{.X1}}" y1="{{.Y1}}" x2="{{.X2}}" y2="{{.Y2}}" class="edge"/>{{end}}
    {{/* узел — переход на свой экран (связи, записи-источники), как строка
         списка: клик, Enter, наведение подгружает экран заранее. Полное имя
         и область — подсказкой: подпись под кружком обрезана */}}
    {{range .Nodes}}
      <g class="node{{if .Dim}} dim{{end}}{{if .Selected}} selected{{end}}"{{if .Addr}} data-goto="{{.Addr}}" tabindex="0" role="link" aria-label="{{.Name}}"{{end}}>
        <title>{{.Name}}{{if .Area}} · {{.Area}}{{end}}</title>
        <circle cx="{{.X}}" cy="{{.Y}}" r="{{.Radius}}"{{if .Color}} style="fill:{{.Color}}"{{end}}/>
        <text x="{{.X}}" y="{{add .Y .Radius 14}}" text-anchor="middle">{{nodeLabel .Name}}</text>
      </g>
    {{end}}
  </svg>
  <figcaption>{{len .Nodes}} из {{.Total}} узлов · крупнее те, у кого больше связей · цвет — область{{if .Legend}}:{{range .Legend}} <span class="area-mark"><span class="dot" style="background:{{.Color}}"></span>{{.Name}}</span>{{end}}{{end}}</figcaption>
</figure>
{{end}}

{{define "field"}}
    <div class="field {{if eq .Kind "toggle"}}field-switch{{end}}{{if or (eq .Kind "code") (eq .Kind "chips") (eq .Kind "checks")}} wide-field{{end}}"{{if .AnchorID}} data-hl="f:{{.AnchorID}}"{{end}}>
      <label for="fld-{{.AnchorID}}">{{.Label}}{{if .Unit}}, {{.Unit}}{{end}}</label>
      {{template "field-control" .}}
      {{if .Hint}}<div class="hint">{{.Hint}}</div>{{end}}
    </div>
    
{{end}}

{{/* Строка настройки (форма со Style «rows»): подпись и подсказка слева,
     один контрол справа, единица рядом с ним. Подсказка в одну строку с
     хвостом, нажатие раскрывает. «По умолчанию» есть, только когда у
     настройки известно умолчание, и видна, пока значение с ним расходится:
     нажатие кладёт умолчание в контрол, а сохраняет его та же нижняя
     полоса, что и любую другую правку. Корень остаётся полем формы (.field):
     полоса считает правки именно по нему */}}
{{define "field-row"}}
    <div class="field setting{{if eq .Kind "toggle"}} setting-switch{{end}}{{if and .HasDefault (not .NotDefault)}} is-default{{end}}"{{if .HasDefault}} data-default="{{.Default}}" data-kind="{{.Kind}}"{{end}}{{if .AnchorID}} data-hl="f:{{.AnchorID}}"{{end}}>
      <label class="setting-label" for="fld-{{.AnchorID}}">{{.Label}}</label>
      {{if .Hint}}<div class="hint setting-hint" tabindex="0" role="button" aria-expanded="false" title="Показать подсказку целиком">{{.Hint}}</div>{{end}}
      <div class="setting-control">
        {{if eq .Kind "number"}}
        <input id="fld-{{.AnchorID}}" class="setting-number" type="number" inputmode="numeric" name="{{.Name}}" value="{{.Value}}"
               {{if .Min}}min="{{.Min}}"{{end}} {{if .Max}}max="{{.Max}}"{{end}} step="1">
        {{else}}{{template "field-control" .}}{{end}}
        {{if .Unit}}<span class="unit">{{.Unit}}</span>{{end}}
        {{if .HasDefault}}<button type="button" class="to-default" title="По умолчанию: {{if .Default}}{{.Default}}{{else}}пусто{{end}}">{{if eq .Kind "timespan"}}круглые сутки{{else}}по умолчанию{{end}}</button>{{end}}
      </div>
    </div>
{{end}}

{{/* Поля формы. Редкие (Field.More) прячутся под «Ещё N полей» в той же
     форме: они уходят вместе с остальными, а раскрывать их человеку нужно
     редко. Раскладка — строками настройки или обычными полями */}}
{{define "form-fields"}}
  {{$rows := eq .Style "rows"}}
  {{range plainFields .Fields}}{{if $rows}}{{template "field-row" .}}{{else}}{{template "field" .}}{{end}}{{end}}
  {{with moreFields .Fields}}
  <details class="more-fields">
    <summary>Ещё {{moreWord (len .)}}</summary>
    <div class="more-body">
      {{range .}}{{if $rows}}{{template "field-row" .}}{{else}}{{template "field" .}}{{end}}{{end}}
    </div>
  </details>
  {{end}}
{{end}}

{{define "field-control"}}
      {{if eq .Kind "segments"}}
        {{$fieldName := .Name}}{{$val := .Value}}
        <div class="segments field-segments" role="radiogroup" aria-label="{{.Label}}">
          {{$tones := .Tones}}
          {{$off := .Disabled}}
          {{range $i, $opt := .Options}}
          <label class="segment{{if eq $opt $val}} selected{{end}}{{with at $tones $i}} tone-{{.}}{{end}}{{if and (has $off $opt) (ne $opt $val)}} disabled{{end}}">
            <input type="radio" name="{{$fieldName}}" value="{{$opt}}" {{if eq $opt $val}}checked{{end}}{{if and (has $off $opt) (ne $opt $val)}} disabled{{end}}>{{$opt}}
          </label>
          {{end}}
        </div>
      {{else if eq .Kind "chips"}}
        {{$fieldName := .Name}}{{$val := .Value}}
        <div class="field-chips">
          {{range .Options}}
          <label class="chip-choice{{if eq . $val}} selected{{end}}">
            <input type="radio" name="{{$fieldName}}" value="{{.}}" {{if eq . $val}}checked{{end}}>{{.}}
          </label>
          {{end}}
        </div>
      {{else if eq .Kind "checks"}}
        {{/* несколько вариантов чипами: у каждого свой флажок «имя.вариант» —
             несколько одноимённых полей форма превращает в одно значение, а
             снятый флажок браузер не шлёт вовсе, и его список идёт в toggles */}}
        {{$fieldName := .Name}}{{$val := .Value}}
        <div class="field-chips" role="group" aria-label="{{.Label}}">
          {{range .Options}}
          <label class="chip-choice{{if chosen $val .}} selected{{end}}">
            <input type="checkbox" name="{{$fieldName}}.{{.}}" value="true" {{if chosen $val .}}checked{{end}}>{{.}}
          </label>
          {{end}}
        </div>
      {{else if eq .Kind "picker"}}
        <select id="fld-{{.AnchorID}}" name="{{.Name}}" {{if .Immediate}}hx-post="/do" hx-target="#window" hx-swap="innerHTML" hx-include="closest form"{{end}}>
          {{$val := .Value}}{{range .Options}}<option value="{{.}}" {{if eq . $val}}selected{{end}}>{{.}}</option>{{end}}
        </select>
      {{else if eq .Kind "toggle"}}
        <input id="fld-{{.AnchorID}}" class="hidden-checkbox" type="checkbox" name="{{.Name}}" value="true" {{if or (eq .Value "on") (eq .Value "true")}}checked{{end}}>
        <label class="field-toggle" for="fld-{{.AnchorID}}" aria-hidden="true"></label>
      {{else if eq .Kind "timespan"}}
        {{/* окно «с … до …»: два браузерных поля времени. Пусто в обоих —
             «круглые сутки». Приходят двумя параметрами, name_from и
             name_to, а склеивает их сервер: своей склейки на клиенте нет */}}
        <span class="timespan" role="group" aria-label="{{.Label}}">
          <input id="fld-{{.AnchorID}}" class="time-input" type="time" name="{{.Name}}_from" value="{{timeFrom .Value}}" aria-label="с">
          <span class="unit" aria-hidden="true">–</span>
          <input class="time-input" type="time" name="{{.Name}}_to" value="{{timeTo .Value}}" aria-label="до">
        </span>
      {{else if eq .Kind "code"}}
        <textarea id="fld-{{.AnchorID}}" class="code" name="{{.Name}}" rows="16">{{.Value}}</textarea>
      {{else if eq .Kind "secure"}}
        {{/* «показать» — не украшение: в поле пароля macOS не даёт включить
             русскую раскладку, и логин кириллицей не набрать. Пометка
             data-secure, а не type, говорит полосе сохранения, что значение
             не выписывать, — тип после «показать» уже text */}}
        <span class="secure-wrap">
          <input id="fld-{{.AnchorID}}" type="password" name="{{.Name}}" value="{{.Value}}" autocomplete="off" data-secure="1">
          <button type="button" class="secure-show" data-show-secure aria-pressed="false" aria-label="Показать набранное">показать</button>
        </span>
      {{else if or (eq .Kind "slider") (eq .Kind "stepper")}}
        <input id="fld-{{.AnchorID}}" type="number" name="{{.Name}}" value="{{.Value}}"
               min="{{.Min}}" {{if .Max}}max="{{.Max}}"{{end}} {{if .Step}}step="{{.Step}}"{{else}}step="any"{{end}}>
      {{else}}
        <input id="fld-{{.AnchorID}}" type="text" name="{{.Name}}" value="{{.Value}}">
      {{end}}
{{end}}

{{define "files"}}
  {{range .}}
    {{if .IsImage}}
      <a href="/api/files/{{.ID}}" target="_blank" rel="noopener"><img class="preview" src="/api/files/{{.ID}}" alt="{{.Name}}" loading="lazy"></a>
    {{else if .ID}}
      <a class="action" href="/api/files/{{.ID}}" target="_blank" rel="noopener">{{.Name}}{{if .Meta}} · {{.Meta}}{{end}}</a>
    {{else}}
      <span class="action" title="{{.Path}}">{{.Name}}</span>
    {{end}}
  {{end}}
{{end}}
`
