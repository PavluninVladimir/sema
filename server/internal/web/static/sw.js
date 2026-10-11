// Служебный работник: оболочка Сёмы должна открываться и без сети, но
// данные не кэшируются никогда — чужой ответ из кэша хуже пустого экрана.
// имя кэша несёт версию сборки: сервер подставляет её при отдаче файла,
// поэтому новая сборка — новый работник, старый кэш стирается при активации
const SHELL_CACHE = 'сёма-оболочка-__СБОРКА__';
// Снимки экранов живут в своём кэше, и его имя нарочно без версии сборки:
// кэш оболочки стирается при каждом выкате, а снимок обязан выкат пережить
// — иначе он пропадал бы ровно тогда, когда нужен (offline-snapshot,
// решение 4). Срок жизни, разрешение хранить и версию словаря называет
// сервер в самой разметке; работник их читает, а не решает сам.
const SNAPSHOTS = 'сёма-снимки';
// «/index.html» в списке нет намеренно: сервер отвечает на него
// переадресацией, и такой ответ в кэше ломает открытие без сети
// файлы оболочки — с версией сборки в адресе, как их просит index.html: по
// такому адресу браузер без работника хранит их год, не спрашивая
const SHELL_FILES = ['/', '/app.js?v=__СБОРКА__', '/style.css?v=__СБОРКА__', '/fonts.css?v=__СБОРКА__',
  '/htmx.js?v=__СБОРКА__', '/icon.svg', '/manifest.webmanifest'];

self.addEventListener('install', (evt) => {
  evt.waitUntil(caches.open(SHELL_CACHE).then((cache) => cache.addAll(SHELL_FILES)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (evt) => {
  evt.waitUntil(
    caches.keys()
      .then((names) => Promise.all(names.filter((nm) => nm !== SHELL_CACHE && nm !== SNAPSHOTS).map((nm) => caches.delete(nm))))
      .then(() => self.clients.claim())
  );
});

// ---- Снимок экрана (offline-snapshot, группа 4) ----
//
// Порядок нерушим: сначала сеть, снимок — только в catch (решение 5). Так
// снимок никогда не подменяет живой экран и не выдаёт себя за свежие данные.
//
// РАЗДЕЛЕНИЕ ТРУДА: складывает снимок страница (app.js), достаёт — работник.
// Кэш общий, ключи общие, а пишет один. Так вышло не из красоты, а из двух
// поломок, каждая проверена в Chrome 27 сентября 2026:
//
//   1. `ответ.clone()` в работнике раздваивает поток тела, и когда запросов
//      `/ui` два разом (опрос открытого чата и переход по меню), чтение
//      странице встаёт насовсем: htmx получает заголовки и не дожидается
//      тела. Переход из чата в «Файлы» не случался вовсе
//      (`TestБраузерПовторныйЗаходВРазговорВнизу`).
//   2. Прочитать тело в работнике и отдать странице свой `new Response`
//      тоже нельзя: у собранного ответа `url` пустой, htmx берёт его за
//      окончательный адрес и стирает из строки браузера хвост `#screen=…`
//      (`TestБраузерЦепочкиНастроек`).
//
// Значит работник обязан отдать странице ровно тот ответ, что пришёл из
// сети, не читая и не пересобирая его. Разметку для снимка страница и так
// держит в руках — она её только что нарисовала, и все признаки сервера
// стоят в ней атрибутами.

// ключ снимка — адрес экрана и версия словаря. Ни поиска, ни предела, ни
// фильтра: это один и тот же экран, а не разные. Те же три строки живут в
// app.js — договор между страницей и работником
const snapshotKey = (dict, screen) =>
  new Request(location.origin + '/снимок?словарь=' + encodeURIComponent(dict) + '&экран=' + encodeURIComponent(screen));
// «какой экран видели последним» и «какой словарь был у сервера в последний
// раз, когда с ним удалось поговорить». Оба — значения сервера, и без сети
// их не знает никто: ни оболочка, ни страница. Поэтому помним сами
const DICT_KEY = '/снимок-словарь';
const LAST_KEY = '/снимок-последний';

const fromCache = async (cache, key) => {
  const was = await cache.match(key);
  return was ? await was.text() : '';
};

const snapshotFor = async (url) => {
  const cache = await caches.open(SNAPSHOTS);
  const dict = await fromCache(cache, DICT_KEY);
  if (!dict) return null;
  // открыли приложение без адреса экрана (ярлык PWA ведёт на «/») —
  // показываем то, что человек видел последним: он ждёт именно этого
  const screen = url.searchParams.get('screen') || await fromCache(cache, LAST_KEY);
  if (!screen) return null;
  const key = snapshotKey(dict, screen);
  const resp = await cache.match(key);
  if (!resp) return null;
  const takenAt = Date.parse(resp.headers.get('X-Sema-Snapshot-At') || '');
  const days = parseInt(resp.headers.get('X-Sema-Snapshot-Days') || '', 10);
  if (!takenAt || !(days > 0) || Date.now() - takenAt > days * 86400000) {
    await cache.delete(key);                              // по сроку снимок стирается, а не показывается устаревшим
    return null;
  }
  return resp;
};

const screenOrSnapshot = async (req, url) => {
  let resp;
  try {
    // ответ уходит странице как есть: ни клона, ни пересборки — см. выше
    resp = await fetch(req);
  } catch (e) {
    const hit = await snapshotFor(url);
    if (hit) return hit;
    throw e;                                             // ни сети, ни снимка — пусть страница скажет это сама
  }
  if (resp.status === 401) {
    // вход кончился (вышли сами или сессию отозвали с другого устройства) —
    // снимка не остаётся: до чужого браузера дотянуться больше нечем.
    // Заголовки читать можно, тело — нет
    caches.delete(SNAPSHOTS).catch(() => {});
  }
  return resp;
};

self.addEventListener('fetch', (evt) => {
  const url = new URL(evt.request.url);
  // данные, действия и файлы — только из сети: кэшировать чужую почту нельзя
  if (url.origin !== location.origin) return;          // чужое не кэшируем вовсе
  // разметка экрана — сеть, а в её отсутствие снимок, если сервер разрешил
  if (url.pathname === '/ui' && evt.request.method === 'GET' && !url.searchParams.get('part')) {
    evt.respondWith(screenOrSnapshot(evt.request, url));
    return;
  }
  // данные и действия — только из сети: чужая переписка из кэша хуже
  // пустого экрана
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/login')) return;
  if (url.pathname === '/ui' || url.pathname === '/do' || url.pathname === '/upload') return;
  if (evt.request.method !== 'GET') return;
  // Оболочка — сначала из кэша этой сборки, и только потом из сети.
  // Раньше было наоборот: кэш выручал лишь без связи, и каждый запуск
  // приложения (в том числе после того, как iOS выгрузил его из памяти)
  // ждал по сети страницу, 174 КБ стилей и 200 КБ клиента — человек всё это
  // время смотрел на белый экран. Данных в оболочке нет (она отдаётся без
  // входа), поэтому вчерашняя её копия ничего не выдаёт за свежее.
  //
  // Свежесть держит имя кэша, а не сверка каждого файла: кэш наполняется
  // целиком при установке работника этой сборки, новая сборка — новый
  // sw.js, новый работник и новый кэш. Подкладывать в кэш ответы сети
  // поверх нельзя: страница одной сборки встретилась бы с клиентом другой.
  // Разошлись сборки оболочки и сервера — app.js узнаёт по X-Sema-Build и
  // дожидается нового работника.
  //
  // Переходом из кэша отвечаем только на «/»: остальные адреса страниц —
  // ручки сервера (возврат входа MCP и другие), им нужна сеть.
  const nav = evt.request.mode === 'navigate';
  if (nav && url.pathname !== '/') return;
  const key = nav ? '/' : evt.request;
  evt.respondWith(
    caches.open(SHELL_CACHE)
      .then((cache) => cache.match(key))
      .then((hit) => hit || fetch(evt.request).then((resp) => {
        // чего в кэше не было (шрифты, значки) — кладём; в кэш только
        // удачные свои ответы: отказ из кэша был бы хуже отсутствия кэша
        if (resp.ok && resp.type === 'basic' && !nav) {
          const copy = resp.clone();
          caches.open(SHELL_CACHE).then((cache) => cache.put(evt.request, copy)).catch(() => {});
        }
        return resp;
      }))
      .catch(() => caches.match(key).then((hit) => hit || caches.match('/')))
  );
});


// ---- Пуши ----
//
// Правила, на которых держится показ (Apple, «Sending web push notifications
// in web apps and browsers»; web.dev про Chrome):
//   1. Каждый пуш показывает уведомление — всегда и сразу. Safari «тихих»
//      пушей не терпит и после нескольких отзывает подписку целиком, а
//      Chrome без показа рисует вместо нашего своё «Сайт обновлён в фоне».
//      Поэтому всё, что может пойти не так (разбор тела, значок), обёрнуто
//      так, чтобы showNotification всё равно состоялся.
//   2. Содержимое уже ограничено сервером (закрытое — только «есть новое»);
//      здесь его только показывают, ничего не добавляя от себя.
//   3. Значок-счётчик ставит сам работник по числу `waiting` из пуша:
//      страница может быть закрыта, а значок — единственное, что остаётся.
//   4. Значок `badge` — монохромный PNG (Android берёт из него только
//      прозрачность), `icon` — цветной PNG: SVG уведомления не рисуются.
const PUSH_ICON = '/icon-192.png';
const PUSH_BADGE = '/icon-badge.png';

// адрес из пуша — только свой: данные приходят с сервера, но открывать по
// нажатию чужой адрес уведомление не должно
const ownUrl = (raw) => {
  try {
    const u = new URL(raw || '/', self.location.origin);
    if (u.origin === self.location.origin) return u.pathname + u.search + u.hash;
  } catch (e) { /* не адрес */ }
  return '/';
};

const readPush = (evt) => {
  if (!evt.data) return {};
  try { return evt.data.json() || {}; } catch (e) { return {}; }
};

const setBadge = async (count) => {
  try {
    const nav = self.navigator;
    if (count > 0 && nav.setAppBadge) await nav.setAppBadge(count);
    else if (count === 0 && nav.clearAppBadge) await nav.clearAppBadge();
  } catch (e) { /* значок не обязателен: уведомление важнее */ }
};

self.addEventListener('push', (evt) => {
  const data = readPush(evt);
  const options = {
    body: data.body ? String(data.body) : '',
    tag: data.tag ? String(data.tag) : 'sema',
    icon: PUSH_ICON,
    badge: PUSH_BADGE,
    lang: 'ru',
    data: { url: ownUrl(data.url) },
    // тот же тег заменяет показанное; со звуком — только когда это тревога
    renotify: !!data.renotify && !!data.tag,
    // висеть, пока не закроют, уведомление заставляет только сервер (поле
    // `sticky`, настройка `push.sticky_alert`, по умолчанию выключена): сам
    // работник по уровню этого не решает. На телефонах поле игнорируется
    requireInteraction: !!data.sticky,
  };
  // нечитаемое тело — всё равно уведомление: «есть новое», без догадок
  const title = data.title ? String(data.title) : 'Сёма: есть новое';
  const jobs = [self.registration.showNotification(title, options)];
  if (typeof data.waiting === 'number') jobs.push(setBadge(data.waiting));
  evt.waitUntil(Promise.all(jobs));
});

// Нажатие открывает нужный экран. Окно уже есть — оно не перезагружается
// (это стёрло бы набранное и открытый чат), а получает письмо с именем
// экрана; окна нет — открывается новое. Окно с видимой страницей — первым.
const openFromNotification = async (target) => {
  const screen = new URLSearchParams(new URL(target, self.location.origin).hash.replace(/^#/, '')).get('screen') || '';
  const all = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  const wins = all.filter((w) => { try { return new URL(w.url).origin === self.location.origin; } catch (e) { return false; } });
  wins.sort((a, b) => (Number(b.focused) - Number(a.focused)) || (Number(b.visibilityState === 'visible') - Number(a.visibilityState === 'visible')));
  const win = wins[0];
  if (win) {
    if (screen) win.postMessage({ sema: 'open', screen });
    try { await win.focus(); } catch (e) { /* система не дала сфокусировать — письмо уже ушло */ }
    return;
  }
  await self.clients.openWindow(target);
};

self.addEventListener('notificationclick', (evt) => {
  evt.notification.close();
  const target = (evt.notification.data && evt.notification.data.url) || '/';
  evt.waitUntil(openFromNotification(target));
});
