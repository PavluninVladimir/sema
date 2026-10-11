// Всё, чего не умеет htmx сам: высота поля ввода, отправка по Enter,
// прокрутка переписки к низу, подсказка «кому уйдёт» и уход на вход при
// отказе по доступу. Рисование разметки — на сервере.

'use strict';

window.sema = {
  // плашка внизу: одно короткое сообщение; прячется при следующей удаче
  banner(text) {
    let node = document.getElementById('link-banner');
    if (!node) {
      node = document.createElement('div'); node.id = 'link-banner'; node.className = 'link-banner';
      document.body.appendChild(node);
    }
    node.textContent = text; node.hidden = false;
  },
  hideBanner() {
    const node = document.getElementById('link-banner');
    if (node) node.hidden = true;
  },

  // поле ввода растёт под текст, но не бесконечно
  field(nodeEl) {
    nodeEl.style.height = 'auto';
    nodeEl.style.height = Math.min(nodeEl.scrollHeight, 140) + 'px';
    sema.recipient(nodeEl.value);
  },

  // Enter отправляет, Shift+Enter переносит строку
  input(evt, nodeEl) {
    if (evt.key === 'Enter' && !evt.shiftKey) {
      evt.preventDefault();
      nodeEl.form.requestSubmit();
    }
  },

  // кому уйдёт написанное: слово вызова в начале строки уводит текст Сёме,
  // а не собеседнику — человек должен видеть это до отправки
  recipient(text) {
    const mark = document.getElementById('recipient');
    if (!mark) return;
    const word = (mark.dataset.callWord || '').toLowerCase().replace(/ё/g, 'е');
    const typed = (text || '').trim().toLowerCase().replace(/ё/g, 'е');
    const toSema = word && typed.startsWith(word);
    mark.textContent = toSema ? 'уйдёт Сёме' : 'уйдёт «' + mark.dataset.to + '»';
    mark.classList.toggle('to-sema', !!toSema);
  },

  // ---- Пуши ----
  //
  // Что на самом деле нужно устройству (developer.apple.com «Sending web
  // push notifications in web apps and browsers», web.dev про Chrome):
  //  · iPhone и iPad: пуши есть только у Сёмы, добавленного на экран «Домой»
  //    (iOS 16.4+). В обычной вкладке Safari PushManager не существует вовсе,
  //    и «браузер пуши не умеет» было бы неправдой — умеет, но не здесь;
  //  · разрешение просится только по касанию, а подписка — сразу за ним, из
  //    того же обработчика: между касанием и subscribe() нельзя ждать сеть.
  //    Поэтому ключ и служебный работник готовятся заранее (pushPrepare);
  //  · разрешение, однажды отклонённое, вернуть из страницы нельзя — только
  //    в настройках системы, и сказать об этом надо словами.
  pushEnv() {
    const ua = navigator.userAgent || '';
    return {
      ios: /iPhone|iPad|iPod/.test(ua) || (/Macintosh/.test(ua) && navigator.maxTouchPoints > 1),
      android: /Android/.test(ua),
      // iOS говорит об этом своим navigator.standalone, остальные — режимом показа
      standalone: navigator.standalone === true || (window.matchMedia && window.matchMedia('(display-mode: standalone)').matches),
    };
  },

  // готовит то, чем подписка пользуется без сети: ключ и работника
  async pushPrepare() {
    if (!sema._pushKey) {
      const resp = await fetch('/api/push/key', { credentials: 'same-origin' });
      if (resp.ok) sema._pushKey = (await resp.json()).key;
    }
    if (!sema._pushReg) sema._pushReg = await sema.pushWorker();
  },

  // работник: ждём не вечно — если регистрация не удалась, ready не наступит никогда
  async pushWorker() {
    const never = new Promise((resolve) => setTimeout(() => resolve(null), 4000));
    return Promise.race([navigator.serviceWorker.ready, never]);
  },

  // что с пушами на этом устройстве: код состояния и слова для человека
  async pushState() {
    const env = sema.pushEnv();
    if (!window.isSecureContext) {
      return { code: 'insecure', button: 'Проверить ещё раз',
        text: 'Пуши работают только по защищённому адресу (https). Откройте Сёму по https://… — по http браузер их не даёт.' };
    }
    if (env.ios && !env.standalone) {
      return { code: 'ios-tab', button: 'Проверить ещё раз',
        text: 'На iPhone и iPad пуши есть только у Сёмы, добавленного на экран «Домой»: в Safari нажмите «Поделиться» → «На экран „Домой“», ' +
          'откройте Сёму оттуда и вернитесь сюда. В обычной вкладке Safari пуши недоступны.' };
    }
    if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) {
      return { code: 'unsupported', button: 'Проверить ещё раз',
        text: env.ios
          ? 'Этой версии iOS пуши недоступны: нужна iOS 16.4 или новее.'
          : 'Этот браузер пуши не умеет. Подойдут Chrome, Edge, Firefox и Safari 16 или новее.' };
    }
    if (Notification.permission === 'denied') {
      return { code: 'denied', button: 'Проверить ещё раз', text: sema.pushDeniedText(env) };
    }
    let reg = null, sub = null;
    try {
      reg = await sema.pushWorker();
      sub = reg && await reg.pushManager.getSubscription();
    } catch (e) {
      return { code: 'unsupported', button: 'Проверить ещё раз', text: 'Служебный работник не запустился: ' + (e && e.message ? e.message : e) };
    }
    if (!reg) {
      return { code: 'unsupported', button: 'Проверить ещё раз', text: 'Служебный работник Сёмы не запущен: пуши без него невозможны. Обновите страницу.' };
    }
    if (!sub) {
      // ключ готовим заранее: подписка должна идти сразу за касанием, без сети между ними
      sema.pushPrepare().catch(() => {});
      return { code: 'off', button: 'Получать пуши',
        text: env.ios
          ? 'Не подписано. Нажмите кнопку — iPhone спросит разрешение один раз; если отказать, вернуть его можно только в настройках iPhone.'
          : 'Не подписано. Нажмите кнопку — браузер спросит разрешение.' };
    }
    // подписка есть в браузере: помнит ли её сервер и под тем ли ключом она сделана
    let info = null;
    try {
      const resp = await fetch('/api/push/key?endpoint=' + encodeURIComponent(sub.endpoint), { credentials: 'same-origin' });
      if (resp.ok) info = await resp.json();
    } catch (e) { /* сервер недоступен — скажем ниже */ }
    if (!info) {
      return { code: 'on', button: 'Отписать это устройство', text: 'Подписано. Сервер сейчас не отвечает — состояние на его стороне не проверено.' };
    }
    sema._pushKey = info.key;
    if (!info.known) {
      // сервер забыл устройство (служба ответила 410, база восстановлена из копии):
      // подписка в браузере цела, и жеста для её повторной передачи не нужно
      try { info = Object.assign(info, await sema.pushSend(sub)); } catch (e) {
        return { code: 'off', button: 'Получать пуши', text: 'Подписка в браузере есть, но сервер её не принял: ' + sema.pushErrorText(e) };
      }
    }
    if (info.same_key === false || sema.pushKeyDiffers(sub, info.key)) {
      return { code: 'resub', button: 'Переподписать',
        text: 'Ключи пушей Сёмы сменились, и прежняя подписка больше не работает. Нажмите кнопку — устройство подпишется заново.' };
    }
    return { code: 'on', button: 'Отписать это устройство',
      text: 'Подписано' + (info.name ? ': «' + info.name + '»' : '') + '. Пуши придут сюда; «Проверить» — в строке этого устройства ниже.' };
  },

  // ключ, под которым сделана подписка, против ключа сервера
  pushKeyDiffers(sub, key) {
    try {
      const was = sub.options && sub.options.applicationServerKey;
      if (!was || !key) return false;
      const have = new Uint8Array(was), want = sema.keyToBytes(key);
      return have.length !== want.length || have.some((byte, i) => byte !== want[i]);
    } catch (e) { return false; }
  },

  pushDeniedText(env) {
    if (env.ios) {
      return 'Уведомления для Сёмы запрещены. Разрешить: «Настройки» → «Уведомления» → «Сёма» → включите «Допуск уведомлений». ' +
        'Если Сёмы в списке нет — удалите его с экрана «Домой», добавьте заново и нажмите кнопку.';
    }
    if (env.android) {
      return 'Уведомления для Сёмы запрещены. Разрешить: значок замка у адреса → «Разрешения» → «Уведомления»; ' +
        'у установленного приложения — «Настройки» → «Приложения» → Сёма → «Уведомления». Потом нажмите кнопку ещё раз.';
    }
    return 'Уведомления для этого сайта запрещены в браузере. Разрешить: значок слева от адреса → «Уведомления» → «Разрешить», потом нажмите кнопку ещё раз.';
  },

  pushErrorText(e) {
    const msg = e && e.message ? e.message : String(e);
    if (e && e.name === 'NotAllowedError') return 'разрешение не дано (' + msg + ')';
    if (e && e.name === 'AbortError') {
      return 'служба пушей на устройстве не ответила (' + msg + '). Проверьте связь и нажмите ещё раз; ' +
        'на Android без сервисов Google пуши Chrome не работают';
    }
    return msg;
  },

  // передать подписку серверу: он выводит имя из заголовка браузера, а страница
  // добавляет то, чего заголовок не знает — режим окна и касания
  async pushSend(sub) {
    const body = sub.toJSON();
    body.standalone = !!sema.pushEnv().standalone;
    body.touch = navigator.maxTouchPoints || 0;
    const res = await fetch('/api/push/subscribe', { method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
    if (!res.ok) throw new Error('сервер не принял подписку: ' + (await res.text()).trim());
    const out = await res.json();
    return { known: true, id: out.id, name: out.name, same_key: true };
  },

  // ---- Предложение подписаться на экране «Ждут вас» ----
  //
  // Туда человек попадает одним касанием, а кнопка в «Настройки →
  // Уведомления» лежит на глубине четырёх шагов (владелец на iPhone до неё
  // не дошёл). Строку рисует сервер скрытой (`.row.offer`), а открывает
  // клиент — подписка живёт в браузере, и сервер про неё не знает. Открыта
  // она, когда человек может что-то сделать: не подписано, ключ сменился,
  // нужен экран «Домой», разрешение отклонено. При «подписано», «не
  // умеет» и «нет https» — скрыта.
  //
  // Мигания нет: состояние помнится между перерисовками экрана и лежит
  // классом на body (см. offerApply).
  offerWants(code) { return code === 'off' || code === 'resub' || code === 'ios-tab' || code === 'denied'; },

  // «Не предлагать на этом устройстве» — решение этого браузера, на сервер не ходит
  offerDismissed() {
    if (sema._offerOff) return true;
    try { return localStorage.getItem('sema.push.offer.off') === '1'; } catch (e) { return false; }
  },

  offerDismiss() {
    sema._offerOff = true;
    try { localStorage.setItem('sema.push.offer.off', '1'); } catch (e) { /* приватное окно: до перезагрузки */ }
    sema._offerOpen = false;
    sema.offerApply(false);
  },

  // открыто или закрыто — класс на body, а не на самой строке: экран
  // перерисовывается подменой разметки, и класс на строке пропадал бы вместе с
  // ней на кадр. Класс на body живёт между перерисовками, и новая строка
  // рисуется уже открытой — без всякого обработчика (так и пропало мигание)
  offerApply(open) {
    document.body.classList.toggle('push-offer', !!open);
  },

  // нарисовать состояние во всех строках пушей: в «Настройки → Уведомления» и
  // в предложении на «Ждут вас». done — подписка только что удалась: строка
  // предложения остаётся открытой, чтобы сказать «Готово», и исчезает при
  // следующей отрисовке экрана
  async pushPaint(extra, done) {
    if (!document.querySelector('[data-action="$push"]')) return null;
    const st = await sema.pushState();
    // кнопки берём после ожидания: экран мог перерисоваться, и прежние узлы мертвы
    document.querySelectorAll('[data-action="$push"]').forEach((button) => {
      const row = button.closest('.row');
      const caption = row && row.querySelector('.caption');
      if (caption) caption.textContent = (extra ? extra + ' ' : '') + st.text;
      button.textContent = st.button;
      button.setAttribute('data-state', st.code);
    });
    const wants = sema.offerWants(st.code) && !sema.offerDismissed();
    sema._offerOpen = wants;
    sema.offerApply(wants || !!done);
    return st;
  },

  // Кнопка. Разрешение просится первым и без единого await перед ним: iOS
  // и Safari принимают requestPermission только в обработчике касания.
  async push(button) {
    const env = sema.pushEnv();
    const possible = window.isSecureContext && !(env.ios && !env.standalone) &&
      'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window;
    const asking = possible && Notification.permission === 'default' ? Notification.requestPermission() : null;
    button.disabled = true;
    try {
      if (asking) await asking;
      let st = await sema.pushState();
      if (st.code === 'on') {
        const reg = await sema.pushWorker();
        const sub = reg && await reg.pushManager.getSubscription();
        if (sub) {
          await fetch('/api/push/unsubscribe', { method: 'POST', credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ endpoint: sub.endpoint }) });
          await sub.unsubscribe();
        }
        await sema.pushPaint('Пуши на этом устройстве выключены.');
      } else if (st.code === 'off' || st.code === 'resub') {
        const reg = await sema.pushWorker();
        const old = reg && await reg.pushManager.getSubscription();
        if (old) await old.unsubscribe();           // подписка под прежним ключом не оживёт
        if (!sema._pushKey) await sema.pushPrepare();
        const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: sema.keyToBytes(sema._pushKey) });
        await sema.pushSend(sub);
        await sema.pushPaint('Готово.', true);
      } else {
        await sema.pushPaint();
      }
    } catch (e) {
      await sema.pushPaint('Не вышло: ' + sema.pushErrorText(e) + '.');
    } finally {
      button.disabled = false;
    }
  },

  // Раз за открытие страницы: подписка есть в браузере, а сервер её забыл, —
  // передаём заново, не дожидаясь, пока человек откроет настройки.
  async pushHeal() {
    if (!window.isSecureContext || !('serviceWorker' in navigator) || !('PushManager' in window) ||
        !('Notification' in window) || Notification.permission !== 'granted') return;
    try {
      const reg = await sema.pushWorker();
      const sub = reg && await reg.pushManager.getSubscription();
      if (!sub) return;
      const resp = await fetch('/api/push/key?endpoint=' + encodeURIComponent(sub.endpoint), { credentials: 'same-origin' });
      if (!resp.ok) return;
      const info = await resp.json();
      sema._pushKey = info.key;
      if (!info.known) await sema.pushSend(sub);
    } catch (e) { /* лечение не обязательно: при нужде спросит сама кнопка */ }
  },

  // ключ VAPID приходит в base64url, а браузеру нужны байты
  keyToBytes(key64) {
    const padding = '='.repeat((4 - key64.length % 4) % 4);
    const b64 = (key64 + padding).replace(/-/g, '+').replace(/_/g, '/');
    const raw = atob(b64);
    return Uint8Array.from([...raw].map(c => c.charCodeAt(0)));
  },

  // Где стоит лента разговора. Перечитывание экрана (опрос, провод,
  // подгрузка ранних) подменяет ленту целиком, и прокрутка сбрасывается в
  // ноль: гадать после подмены, был ли человек внизу, нельзя — лента уже
  // новая. Раньше так и гадали, и признак «был внизу» залипал: второй заход
  // в разговор открывался на первом сообщении. Поэтому место запоминается
  // до подмены (запомнитьМесто на htmx:before:swap) и восстанавливается
  // после. Новый показ — пришли с другого экрана — всегда на последнем.
  _screen: '',
  _place: null,
  _loading: false,
  bottom() {
    const feed = document.getElementById('feed');
    if (!feed) return;
    const screen = screenNow();
    const place = sema._place;
    sema._place = null;
    sema._loading = false;
    if (screen !== sema._screen || !place || place.screen !== screen || place.atBottom) {
      sema._screen = screen;
      feed.scrollTop = feed.scrollHeight;
      sema.keepBottomOnImages(feed);
      return;
    }
    // человек читал историю или подгружал ранние: ранние встали сверху,
    // а расстояние до низа осталось прежним — место чтения не уехало
    feed.scrollTop = feed.scrollHeight - place.fromBottom;
  },
  // картинка в пузыре догружается после прыжка вниз и выталкивает
  // последнее сообщение из вида: лента канала открывалась на середине
  // последнего поста. Кто был у низа до картинки — остаётся у низа
  keepBottomOnImages(feed) {
    feed.querySelectorAll('img').forEach((img) => {
      if (img.complete) return;
      img.addEventListener('load', () => {
        const toBottom = feed.scrollHeight - feed.scrollTop - feed.clientHeight - img.clientHeight;
        if (toBottom < 200) feed.scrollTop = feed.scrollHeight;
      }, { once: true });
    });
  },
  rememberPlace() {
    const feed = document.getElementById('feed');
    if (!feed) return;
    const fromBottom = feed.scrollHeight - feed.scrollTop;
    sema._place = { screen: screenNow(), fromBottom, atBottom: fromBottom - feed.clientHeight < 200 };
  },
};

// Клиентские действия. Сервер помечает их знаком «$»: они не ходят на /do,
// их делает браузер сам. Переход по строке, открытие файла, «показать
// ещё», копирование, внешняя ссылка.
const screenNow = () => (document.getElementById('screen') || {}).getAttribute?.('data-screen') || '';
const openScreen = (url, limit, source, withFilter) => {
  let path = '/ui?screen=' + encodeURIComponent(url);
  if (limit) path += '&limit=' + limit;
  if (withFilter) {
    // «показать ещё» остаётся на том же чипе и с тем же поиском
    const sect = document.getElementById('screen'), search = document.getElementById('search');
    if (sect && sect.dataset.filter) path += '&filter=' + encodeURIComponent(sect.dataset.filter);
    if (search && search.value) path += '&search=' + encodeURIComponent(search.value);
  }
  // источник — сам нажатый элемент: без него htmx считает источником всё
  // тело страницы и подмешивает в запрос поля всех форм на экране. Запасной
  // источник — #окно, а не #экран: у экрана чата свои hx-select/hx-target,
  // и через них «назад» вставлял бы в окно одну ленту без меню и шапки
  htmx.ajax('GET', path, { source: source || document.getElementById('window'), target: '#window', swap: 'innerHTML' });
};
// ссылка на названии строки — не «управление внутри строки», а сам её
// переход: она для клавиатуры и чтения вслух, а ведёт туда же, куда клик
// по строке. Разбирает её тот же обработчик, иначе адрес поменялся бы в
// строке браузера, а экран остался прежним
const insideControl = (node) => {
  const u = node.closest('button, a, select, input, textarea, summary, label, details.row-menu');
  return !!u && !u.classList.contains('row-link');
};

document.body.addEventListener('keydown', (evt) => {
  if (evt.key !== 'Enter' && evt.key !== ' ') return;
  const row = evt.target.closest && evt.target.closest('.row.clickable, .graph .node[data-goto]');
  if (!row || row !== evt.target) return;
  evt.preventDefault();
  // у узла графа (SVG) своего click() нет — шлём событие сами
  row.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
});
document.body.addEventListener('click', (evt) => {
  const target = evt.target;
  // узел графа памяти — переход на его экран, как строка списка
  const graphNode = target.closest && target.closest('.graph .node[data-goto]');
  if (graphNode) { openScreen(graphNode.getAttribute('data-goto'), 0, graphNode); return; }
  // клик по строке с переходом — если не по кнопке внутри неё
  const row = target.closest('.row.clickable');
  if (row && !insideControl(target)) {
    // ссылку названия ведём сами: её href нужен для клавиатуры и для
    // «открыть в новой вкладке», а обычный переход делает клиент
    if (target.closest('a.row-link')) evt.preventDefault();
    if (row.dataset.goto) { openScreen(row.dataset.goto, 0, row); return; }
    if (row.dataset.file) { window.open('/api/files/' + row.dataset.file, '_blank'); return; }
  }
  const button = target.closest('[data-action]');
  if (!button) return;
  evt.preventDefault();
  const what = button.dataset.action, where = button.dataset.target || '';
  const sect = document.getElementById('screen');
  switch (what) {
    case '$more': {
      // внутри ленивого раздела «ещё» просит у сервера ту же часть с
      // удвоенным пределом, а не весь экран заново
      const fragment = button.closest('details.fragment');
      if (fragment) { sema.moreInFragment(fragment); break; }
      const was = parseInt(sect && sect.dataset.limit, 10) || 50;
      openScreen(screenNow(), was * 2, button, true);
      break;
    }
    case '$file': window.open('/api/files/' + where, '_blank'); break;
    case '$open': if (/^https?:\/\//.test(where)) window.open(where, '_blank', 'noopener'); break;
    case '$copy': navigator.clipboard && navigator.clipboard.writeText(where); break;
    // скопировать значение поля этого экрана по его имени: текст уже лежит в поле, и
    // носить его второй раз в самой кнопке незачем (текст скилла — 30 КБ). Копируется
    // то, что в поле сейчас, — и сохранённое, и набранное
    case '$copy_field': {
      const field = sect && Array.from(sect.querySelectorAll('textarea[name], input[name]')).find((el) => el.name === where);
      if (field && navigator.clipboard) navigator.clipboard.writeText(field.value);
      break;
    }
    case '$push': sema.push(button); break;
    case '$push_later': sema.offerDismiss(); break;
    default: console.warn('действие только для приложения на маке:', what);
  }
});

// Вопрос перед действием. hx-prompt в htmx 4 нет, поэтому спрашиваем сами
// своим окном: закрытый ввод для ключей, своя надпись на кнопке, начальное
// значение. Ответ уходит полем value — как у клиента на маке.
// Ловим на стадии захвата, чтобы htmx не успел отправить запрос без ответа.
document.body.addEventListener('click', async (evt) => {
  const button = evt.target.closest('[data-prompt]');
  if (!button) return;
  evt.preventDefault();
  evt.stopImmediatePropagation();
  const resp = await sema.ask(button.dataset.prompt, button.dataset.value || '',
    !!button.dataset.masked, button.dataset.confirmOk || 'Сохранить');
  if (resp === null) return;
  let values = {};
  try { values = JSON.parse(button.getAttribute('hx-vals') || '{}'); } catch (e) {}
  values.value = resp;
  values.screen = screenNow();
  htmx.ajax('POST', '/do', { source: button, target: '#window', swap: 'innerHTML', values: values });
}, true);

// Подтверждение перед необратимым. Браузерное окно приходит с чужой
// шапкой «Подтвердите действие на sema.local:8443» и кнопкой «OK» — в нём
// человек не читает вопрос, а ищет, где согласиться. Спрашиваем своим
// окном, надпись на кнопке — та, что назвал сервер («Убрать», «Забыть»).
document.body.addEventListener('click', async (evt) => {
  const button = evt.target.closest('[data-confirm]');
  if (!button || button.dataset.prompt) return; // у вопроса с полем своё окно
  evt.preventDefault();
  evt.stopImmediatePropagation();
  const yes = await sema.confirm(button.dataset['confirm'],
    button.dataset['confirmOk'] || 'Да', !!button.dataset['danger']);
  if (!yes) return;
  let values = {};
  try { values = JSON.parse(button.getAttribute('hx-vals') || '{}'); } catch (e) {}
  values.screen = screenNow();
  htmx.ajax('POST', '/do', { source: button, target: '#window', swap: 'innerHTML', values: values });
}, true);

// окно подтверждения: тот же <dialog>, что и вопрос, но без поля ввода
sema.confirm = (text, okLabel, danger) => new Promise((resolve) => {
  let win = document.getElementById('confirm');
  if (!win) {
    win = document.createElement('dialog');
    win.id = 'confirm';
    win.className = 'ask';
    win.innerHTML = '<div class="ask-body"><p class="ask-text"></p>' +
      '<div class="right-side"><button type="button" class="action cancel">Отмена</button>' +
      '<button type="button" class="action primary"></button></div></div>';
    document.body.appendChild(win);
  }
  const ok = win.querySelector('button.primary');
  const cancelBtn = win.querySelector('button.cancel');
  win.querySelector('.ask-text').textContent = text;
  ok.textContent = okLabel;
  ok.classList.toggle('danger', !!danger);
  let settled = false;
  const finish = (resp) => {
    if (settled) return;
    settled = true;
    try { win.close(); } catch (e) {}
    resolve(resp);
  };
  ok.onclick = () => finish(true);
  cancelBtn.onclick = () => finish(false);
  win.onkeydown = (ev) => {
    if (ev.key === 'Escape') finish(false);
    if (ev.key === 'Enter') finish(true);
  };
  win.showModal();
  ok.focus();
});

// окно вопроса: <dialog> строится один раз и живёт вне подменяемого окна.
// На событие close не полагаемся — в этой странице оно не доходит до
// слушателей (htmx его перехватывает), поэтому ответ снимаем с самих
// кнопок и с Esc.
sema.ask = (question, value, masked, okLabel) => new Promise((resolve) => {
  let win = document.getElementById('ask');
  if (!win) {
    win = document.createElement('dialog'); win.id = 'ask'; win.className = 'ask';
    win.innerHTML = '<div class="ask-body"><p class="ask-text" id="ask-text"></p><input class="ask-input" autocomplete="off" aria-labelledby="ask-text">' +
      '<div class="right-side"><button type="button" class="action cancel">Отмена</button><button type="button" class="action primary"></button></div></div>';
    document.body.appendChild(win);
  }
  const input = win.querySelector('.ask-input');
  const ok = win.querySelector('button.primary');
  const cancelBtn = win.querySelector('button.cancel');
  win.querySelector('.ask-text').textContent = question;
  input.type = masked ? 'password' : 'text';
  input.value = value;
  ok.textContent = okLabel;
  let settled = false;
  const finish = (resp) => {
    if (settled) return;
    settled = true;
    ok.onclick = cancelBtn.onclick = win.onkeydown = null;
    if (win.open) win.close();
    resolve(resp);
  };
  ok.onclick = () => finish(input.value);
  cancelBtn.onclick = () => finish(null);
  win.onkeydown = (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(input.value); }
    if (e.key === 'Escape') { e.preventDefault(); finish(null); }
  };
  win.showModal();
  input.focus(); input.select();
});


// Опоздавший ответ окна не подменяет свежий экран. Переход по меню, «ещё»
// и подгрузка ранних сообщений чата пишут в одно окно (#window), и
// побеждал пришедший последним: долистал чат до верха — клиент сам попросил
// ранние сообщения, — нажал «Файлы», и ответ подгрузки возвращал чат
// (4 октября 2026, TestBrowserReenterConversationAtBottom стал падать, как
// только экраны ускорились). Новый запрос в окно уводит ответы прежних,
// ещё не вернувшихся, в отвязанный от страницы узел: отбросить опоздавший
// ответ при вставке нельзя — его кнопка к тому времени уже выпала из
// страницы вместе со старым окном, события её запроса до body не доходят, а
// htmx вставляет ответ всё равно. Обрывать запрос тоже нельзя: htmx пишет
// оборванный в консоль как ошибку, а действие, уже ушедшее на сервер,
// выглядело бы неушедшим. Опрос экрана пишет в #feed, а не в окно, — его это
// не касается.
//
// sema.windowBusy — в окно идёт запрос: человек уходит с экрана, и ленивая
// подгрузка ранних сообщений (ниже) не стартует.
(() => {
  const inFlight = new Set();
  const toWindow = (ctx) => !!(ctx && ctx.target && ctx.target.id === 'window');
  document.body.addEventListener('htmx:before:request', (evt) => {
    const ctx = evt.detail && evt.detail.ctx;
    if (!toWindow(ctx)) return;
    inFlight.forEach((old) => {
      if (old === ctx) return;
      old.target = document.createElement('div'); // ответ уйдёт в никуда
      old.selectOOB = undefined;
      inFlight.delete(old);
    });
    inFlight.add(ctx);
  });
  // на document, а не на body: событие запроса, чья кнопка выпала из
  // страницы вместе со старым окном, htmx шлёт прямо в document — до body оно
  // не доходит, и счётчик залипал бы навсегда (ревью perf-pass 5)
  document.addEventListener('htmx:finally:request', (evt) => {
    const ctx = evt.detail && evt.detail.ctx;
    if (ctx) inFlight.delete(ctx);
  });
  sema.windowBusy = () => inFlight.size > 0;
})();

// ---- Предзагрузка экрана (perf-pass 7) ----
//
// Наведение мыши на пункт меню, строку или вкладку (сто миллисекунд — не
// пролёт курсора) и касание на телефоне заранее просят экран, а нажатие
// берёт готовый ответ: по Wi-Fi до Pi это сотни миллисекунд, которые
// человек раньше ждал после нажатия. Просить можно только экраны, чьё
// открытие ничего не меняет, — их приставки сервер называет атрибутом
// data-prefetch (чат ставит «прочитано», «Почта» сдвигает окно); срок —
// data-prefetch-ms, ноль выключает. Любое действие (POST), кадр провода,
// загрузка из очереди и смена раскладки подогретое сбрасывают: ответ,
// собранный до них, показывал бы прежнее. Подогрев сервер собирает, ничего
// не делая наружу; экрану, которому без сети показать нечего, он отвечает
// 204 — и нажатие идёт обычным запросом.
(() => {
  const ready = new Map(); // адрес запроса → { at, resp }
  const sect = () => document.getElementById('screen');
  const ttl = () => { const sc = sect(); return sc ? parseInt(sc.getAttribute('data-prefetch-ms'), 10) || 0 : 0; };
  const allowed = (url) => {
    const sc = sect();
    if (!sc) return false;
    let params;
    try { params = new URL(url, location.origin).searchParams; } catch (e) { return false; }
    // только адрес экрана — ни отбора, ни поиска, ни порции: «Здоровье» с
    // периодом его запоминает
    if ([...params.keys()].some((k) => k !== 'screen')) return false;
    const addr = params.get('screen') || '';
    const list = (sc.getAttribute('data-prefetch') || '').split(' ').filter(Boolean);
    return !!addr && list.some((p) => addr === p || addr.startsWith(p));
  };
  // адрес так, как его попросит сам переход: hx-get у пункта меню и вкладки,
  // у строки — тот, что строит openScreen
  const urlOf = (el) => {
    const hx = el.getAttribute('hx-get');
    if (hx && hx.startsWith('/ui?')) return hx;
    const go = el.getAttribute('data-goto');
    return go ? '/ui?screen=' + encodeURIComponent(go) : '';
  };
  const warm = (el) => {
    const url = urlOf(el), ms = ttl();
    if (!url || !ms || !allowed(url)) return;
    const was = ready.get(url);
    if (was && Date.now() - was.at < ms) return;
    const headers = { 'HX-Request': 'true', 'HX-Request-Type': 'partial' };
    const sc = sect();
    if (sc && sc.dataset.screen) headers['HX-Current-Screen'] = encodeURIComponent(sc.dataset.screen);
    if (phone()) headers['X-Sema-Phone'] = '1';
    headers['X-Sema-Prefetch'] = '1';
    const resp = fetch(url, { headers, credentials: 'same-origin' });
    resp.catch(() => ready.delete(url));
    ready.set(url, { at: Date.now(), resp });
  };
  const target = (node) => node && node.closest &&
    node.closest('#menu button.item[hx-get], .row.clickable[data-goto], a.segment[hx-get], .graph .node[data-goto]');
  let hover = null;
  document.addEventListener('mouseover', (evt) => {
    const el = target(evt.target);
    clearTimeout(hover);
    if (el) hover = setTimeout(() => warm(el), 100);
  });
  document.addEventListener('pointerdown', (evt) => {
    if (evt.pointerType === 'mouse') return; // у мыши — наведение
    const el = target(evt.target);
    if (el) warm(el);
  }, { passive: true });
  document.body.addEventListener('htmx:before:request', (evt) => {
    const ctx = evt.detail && evt.detail.ctx;
    if (!ctx || !ctx.request) return;
    if (String(ctx.request.method || 'GET').toUpperCase() !== 'GET') { ready.clear(); return; }
    const hit = ready.get(ctx.request.action);
    if (!hit) return;
    ready.delete(ctx.request.action);
    if (Date.now() - hit.at > ttl()) return;
    // от подогрева отказались (204) или он не дошёл — обычный запрос
    ctx.fetch = (url, init) => hit.resp.then(
      (r) => (r.status === 204 ? fetch(url, init) : r),
      () => fetch(url, init));
  });
  sema.prefetched = () => [...ready.keys()];
  sema.prefetchDrop = () => ready.clear();
})();

// Занятость: нажатое не нажимается второй раз, пока запрос идёт. Тихая
// кнопка заставляет жать её трижды — и трижды делать действие.
// Секция экрана опрашивает ленту сама — её не гасим, иначе чат мигает
// каждые четыре секунды и не нажимается на медленной связи
const pressable = (node) => !!(node && node.matches && node.matches('button, form, select, input, textarea, a'));
document.body.addEventListener('htmx:before:request', (evt) => {
  const node = evt.detail && evt.detail.ctx && evt.detail.ctx.sourceElement;
  if (pressable(node)) node.classList.add('busy');
});
document.body.addEventListener('htmx:after:request', (evt) => {
  const ctx = evt.detail && evt.detail.ctx;
  const node = ctx && ctx.sourceElement;
  if (node && node.classList) node.classList.remove('busy');
  if (ctx && ctx.response && ctx.response.status > 0 && ctx.response.status < 300) sema.hideBanner();
  // отправленное из переписки стирается только если ушло: неудачу
  // человек должен видеть вместе с тем, что писал
  const field = document.getElementById('input');
  if (field && node && node.classList && node.classList.contains('composer') && ctx.response && ctx.response.status < 300) {
    field.value = '';
    sema.field(field);
    drafts.save(screenNow(), '');
  }
});

// Набранное переживает подмену окна без hx-preserve: тот переносил старый
// элемент целиком, вместе с hx-get прежнего экрана и чужим текстом —
// набор на «Файлах» перезапрашивал «Почту», черновик уезжал в другой чат.
// Помним текст сами и кладём его в новый элемент того же экрана.
//
// Черновик обязан пережить не только подмену окна, но и перезагрузку
// страницы и закрытие приложения (offline-snapshot, 5.1) — объекта в
// памяти вкладки для этого мало. Хранилище — то же самое, где лежит и
// очередь исходящих ниже по файлу (offline-snapshot, решение 6: одно
// место на диске на обоих). Диск может быть недоступен (приватное окно,
// квота, старый Safari) — это не ошибка, а обычный случай: каждый вызов
// обёрнут так, чтобы страница работала и без него, просто черновик
// тогда не переживёт перезагрузку — как и localStorage ниже по файлу.
const OUTBOX_DB = 'сёма-исходящие';
let outboxDBPromise = null;
const outboxDB = () => {
  if (outboxDBPromise) return outboxDBPromise;
  outboxDBPromise = new Promise((resolve) => {
    if (!('indexedDB' in window)) { resolve(null); return; }
    let opening;
    try { opening = indexedDB.open(OUTBOX_DB, 1); } catch (e) { resolve(null); return; }
    opening.onupgradeneeded = () => {
      const db = opening.result;
      // черновик — по экрану (одна запись на разговор), очередь — по
      // возрастающему номеру: порядок постановки и есть ключ
      if (!db.objectStoreNames.contains('черновики')) db.createObjectStore('черновики');
      if (!db.objectStoreNames.contains('очередь')) db.createObjectStore('очередь', { keyPath: 'id', autoIncrement: true });
    };
    opening.onsuccess = () => resolve(opening.result);
    opening.onerror = () => resolve(null);
    opening.onblocked = () => resolve(null);
  });
  return outboxDBPromise;
};
const idbResult = (req) => new Promise((ok, reject) => {
  req.onsuccess = () => ok(req.result);
  req.onerror = () => reject(req.error);
});
const idbStore = async (name, mode) => {
  const db = await outboxDB();
  if (!db) return null;
  try { return db.transaction(name, mode).objectStore(name); } catch (e) { return null; }
};
const drafts = {
  async get(screen) {
    try {
      const os = await idbStore('черновики', 'readonly');
      if (!os) return '';
      return (await idbResult(os.get(screen))) || '';
    } catch (e) { return ''; }
  },
  async save(screen, text) {
    try {
      const os = await idbStore('черновики', 'readwrite');
      if (!os) return;
      if (text) os.put(text, screen); else os.delete(screen);
    } catch (e) { /* диск недоступен — черновик просто не переживёт перезагрузку */ }
  },
};
// Черновики и очередь — это текст писем человека и его файлы, лежащие на
// диске устройства. Значит они уходят там же, где уходит снимок: когда
// сессии больше нет и когда устройство не помечено своим (offline-snapshot
// 4.4 и сценарий «Чужая машина: содержимого его писем и файлов на этой
// машине не остаётся»). Отметка «своё устройство» живёт у сессии на
// сервере, и после её отзыва дотянуться до чужого браузера больше нечем —
// стирать надо на этой стороне и сразу.
// Стирания ждём по-настоящему (итогЗапросаIDB), а не просто просим: сразу
// после него страница уходит на вход, а незакрытая транзакция IndexedDB при
// уходе со страницы отменяется — письма остались бы лежать на чужой машине,
// и никто бы этого не заметил.
const wipeOutbox = async () => {
  for (const name of ['черновики', 'очередь']) {
    try {
      const os = await idbStore(name, 'readwrite');
      if (os) await idbResult(os.clear());
    } catch (e) { /* хранилища нет — стирать нечего */ }
  }
};
let searchWas = { screen: '', text: '', focused: false };
document.body.addEventListener('input', (evt) => {
  const node = evt.target;
  if (node.id === 'search') searchWas = { screen: screenNow(), text: node.value, focused: true };
  if (node.id === 'input') drafts.save(screenNow(), node.value);
});
document.body.addEventListener('htmx:after:swap', () => {
  const screen = screenNow();
  const search = document.getElementById('search');
  if (search && searchWas.screen === screen && searchWas.text !== search.value) {
    search.value = searchWas.text;
  }
  if (search && searchWas.screen === screen && searchWas.focused && document.activeElement !== search) {
    search.focus(); search.setSelectionRange(search.value.length, search.value.length);
  }
  if (searchWas.screen !== screen) searchWas = { screen: '', text: '', focused: false };
  const input = document.getElementById('input');
  // чтение с диска асинхронное: подставляем, только если поле всё ещё
  // пустое и экран не успел смениться, пока читали
  if (input && !input.value) {
    drafts.get(screen).then((text) => {
      if (text && screenNow() === screen && document.getElementById('input') === input && !input.value) {
        input.value = text; sema.field(input);
      }
    });
  }
});

// ---- Очередь отложенных действий (offline-snapshot, группа 5) ----
//
// Сервер помечает откладываемым ровно два действия — отправку сообщения
// в чат и прикрепление файла (Action.Deferrable, design offline-snapshot
// решение 7). До группы 4 признак до браузера не доезжал вовсе, и очередь
// узнавала эти два места по разметке — форме `.композер` и скрепке. Теперь
// признак есть: `data-deferrable` у самого элемента, который шлёт
// действие. Так решает сервер, а не клиент: станет откладываемым что-то
// третье — очередь подхватит его сама, не меняя ни строки.
// Всё остальное — подтверждение команды, спам, отпечаток, права,
// удаление — идёт обычным hx-post и без связи не откладывается вовсе:
// это решения этого мгновения, а не очереди (решение 7 там же).
//
// Состояния записи и почему их четыре:
//   ожидает     — связи нет, запись ждёт её и уйдёт сама;
//   уходит      — прямо сейчас летит (кто именно шлёт, помечено вкладкой);
//   сомнительно — ушла в сеть, а ответа нет: Сёма мог её выполнить, а мог
//                 и не получить вовсе. Повтор дал бы второе сообщение,
//                 поэтому решает человек;
//   отказ       — Сёма ответил «нельзя, вот почему». Это не «ждёт связи»:
//                 повторять такое каждые двадцать секунд бессмысленно, и
//                 остальную очередь оно держать не должно.
(() => {
  let online = true; // лучшая догадка о связи: её подтверждают и опровергают настоящие запросы, а не обещание браузера
  const offlineNow = () => !online || navigator.onLine === false;

  // Метка вкладки живёт в sessionStorage: она переживает перезагрузку этой
  // вкладки и не видна соседним — ровно то, чем отличается «мою отправку
  // оборвала перезагрузка» от «прямо сейчас шлёт другая вкладка». Без этого
  // различения одно лечилось ценой другого: либо запись висела «уходит» до
  // перезагрузки второй вкладки и держала её очередь, либо вторая вкладка
  // объявляла сомнительной честно летящую отправку, и «Отправить всё равно»
  // давало дубль.
  const myTab = (() => {
    const fresh = () => Math.random().toString(36).slice(2) + Date.now().toString(36);
    try {
      let m = sessionStorage.getItem('сёма-вкладка');
      if (!m) { m = fresh(); sessionStorage.setItem('сёма-вкладка', m); }
      return m;
    } catch (e) { return fresh(); }
  })();
  const QUEUE_LOCK = 'сёма-исходящие';
  const LEASE_MS = 90000; // сколько ждать чужую отправку, когда замка нет
  const HEARTBEAT_MS = 20000; // и как часто продлевать свою, пока она идёт

  const queueStore = (mode) => idbStore('очередь', mode);
  const queueList = async () => {
    try {
      const os = await queueStore('readonly');
      if (!os) return [];
      const all = await idbResult(os.getAll());
      return (all || []).sort((a, b) => a.id - b.id);
    } catch (e) { return []; }
  };
  const queueAdd = async (record, status) => {
    try {
      const os = await queueStore('readwrite');
      if (!os) return null;
      return await idbResult(os.add({ ...record, поставлено: new Date().toISOString(), статус: status || 'ожидает' }));
    } catch (e) { return null; }
  };
  const queueSetStatus = async (id, status, err) => {
    try {
      const os = await queueStore('readwrite');
      if (!os) return;
      const rec = await idbResult(os.get(id));
      if (!rec) return;
      rec.статус = status;
      rec.ошибка = err || '';
      os.put(rec);
    } catch (e) {}
  };
  // продлить — пока отправка идёт, аренда обновляется: иначе долгая
  // загрузка файла выглядела бы для соседней вкладки брошенной, и та
  // объявила бы её сомнительной ровно посреди честного запроса
  const renewLease = async (id) => {
    try {
      const os = await queueStore('readwrite');
      if (!os) return;
      const rec = await idbResult(os.get(id));
      if (rec && rec.статус === 'уходит' && rec.вкладка === myTab) { rec.взято = new Date().toISOString(); os.put(rec); }
    } catch (e) {}
  };
  const queueRemove = async (id) => {
    try {
      const os = await queueStore('readwrite');
      if (!os) return;
      os.delete(id);
    } catch (e) {}
  };
  // Взять самую раннюю запись и тут же пометить «уходит» одной
  // транзакцией: IndexedDB сериализует readwrite-транзакции одного
  // хранилища между вкладками, и вторая вкладка увидит уже «уходит» —
  // это и есть защита от двойной отправки, когда открыто две вкладки.
  //
  // Записи, ждущие решения человека («сомнительно», «отказ»), пропускаются:
  // порядок постановки важен, но не настолько, чтобы один вопрос без ответа
  // держал всё написанное после него. Летящую («уходит») пропускать нельзя —
  // на ней останавливаемся и ждём: она уйдёт через мгновение, и порядок
  // сохранится.
  const queueTakeFirst = async () => {
    try {
      const os = await queueStore('readwrite');
      if (!os) return null;
      const all = (await idbResult(os.getAll())) || [];
      for (const rec of all.sort((a, b) => a.id - b.id)) {
        if (rec.статус === 'уходит') return { record: rec, taken: false };
        if (rec.статус !== 'ожидает') continue;
        rec.статус = 'уходит';
        rec.вкладка = myTab;
        rec.взято = new Date().toISOString();
        rec.ошибка = '';
        os.put(rec);
        return { record: rec, taken: true };
      }
      return null;
    } catch (e) { return null; }
  };

  // Брошенная отправка — «уходит» без живого отправителя. Три признака, и
  // все три — про то, кто её вёл, а не про то, дошла ли она: дошла или нет,
  // отсюда не видно вовсе, поэтому итог один — спросить человека.
  const releaseAbandoned = async (lockHeld) => {
    for (const rec of await queueList()) {
      if (rec.статус !== 'уходит') continue;
      const mine = rec.вкладка === myTab;       // моя, а в этой вкладке её никто не ведёт: оборвала перезагрузка
      const takenAt = Date.parse(rec.взято || '') || 0;
      if (!mine && !lockHeld && Date.now() - takenAt <= LEASE_MS) continue;
      await queueSetStatus(rec.id, 'сомнительно');
    }
  };

  // Замок браузера снимается сам, когда вкладка умирает: это единственный
  // признак «отправитель ещё жив», который не врёт при спящем ноутбуке и
  // усыплённых в фоне таймерах. Вне защищённого происхождения его нет вовсе
  // (Сёма по http://sema.local — как раз такой случай), поэтому рядом всегда
  // работает аренда по времени: грубее, зато есть везде.
  const underLock = async (job) => {
    const locks = navigator.locks;
    if (!locks || !locks.request) return job(false);
    try {
      return await locks.request(QUEUE_LOCK, { ifAvailable: true },
        (lock) => (lock ? job(true) : undefined)); // замок занят — шлёт другая вкладка, второй гонец только навредит
    } catch (e) { return job(false); }
  };

  const shortTime = (iso) => {
    try { return new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }); }
    catch (e) { return ''; }
  };

  // связьЕсть — дешёвая проба: отвечает ли Сёма прямо сейчас. Нужна не для
  // красоты, а чтобы отличить два случая, которые у fetch выглядят одним
  // исключением: «запрос никуда не ушёл, связи нет» и «запрос ушёл, а ответ
  // пропал». Первый повторяется сам, второй повторять нельзя — Сёма мог
  // его выполнить, и повтор дал бы второе сообщение. `/api/health` работник
  // из кэша не отдаёт никогда, так что проба проверяет сеть, а не кэш.
  const linkAlive = async () => {
    try {
      await fetch('/api/health', { cache: 'no-store', credentials: 'same-origin' });
      return true; // любой ответ — это ответ: связь есть, даже если он «нельзя»
    } catch (e) { return false; }
  };

  // Итог отправки одной записи. Четыре разных исхода вместо прежнего
  // «да/нет»: раньше отказ Сёмы («чата больше нет») был неотличим от
  // отсутствия связи, подписывался «ждёт связи» — неправдой — и повторялся
  // каждые двадцать секунд вечно, держа за собой всю очередь.
  const queueSendRecord = async (rec) => {
    if (!(await linkAlive())) { online = false; return { noLink: false }; }
    online = true;
    let resp;
    try {
      if (rec.вид === 'файл') {
        const data = new FormData();
        data.append('файл', rec.файл, rec.имяФайла);
        resp = await fetch('/upload?screen=' + encodeURIComponent(rec.экран), {
          method: 'POST', body: data, credentials: 'same-origin', headers: { 'HX-Request': 'true' },
        });
      } else {
        const data = new FormData();
        data.set('action', rec.действие);
        data.set('target', rec.цель);
        data.set('screen', rec.экран);
        data.set(rec.поле, rec.текст);
        // X-Sema-Save просит у /do короткий JSON {ок,ошибка} вместо целого
        // экрана: запрос уходит не из просмотра этого чата (человек мог
        // уже уйти в другой разговор или закрыть приложение), и разбирать
        // HTML-подмену тут незачем — если чат открыт сейчас, его обновит
        // обычный htmx-запрос следом
        resp = await fetch('/do', {
          method: 'POST', body: data, credentials: 'same-origin',
          headers: { 'HX-Request': 'true', 'X-Sema-Save': '1' },
        });
      }
    } catch (e) {
      // Мгновение назад Сёма отвечал, а этот запрос оборвался: он мог
      // дойти и выполниться. Повторять нельзя, молчать тоже — спрашиваем.
      return { doubtful: true };
    }
    online = true; // ответ пришёл — связь есть, даже если Сёма отказал
    // отложенное действие идёт мимо htmx: подогретое до него устарело
    if (sema.prefetchDrop) sema.prefetchDrop();
    if (resp.status === 401) return { login: true };
    if (!resp.ok) return { refused: 'Сёма ответил отказом ' + resp.status };
    if (rec.вид === 'файл') return { sent: true };
    const body = await resp.json().catch(() => ({}));
    if (body['ок']) return { sent: true };
    return { refused: body['ошибка'] || 'Сёма не принял это и не сказал почему' };
  };

  let recentlySent = []; // {экран, вид, текст/имяФайла, поставлено, отправлено} — только подпись в панели
  let queueRunning = false;
  const queuePass = async (lockHeld) => {
    await releaseAbandoned(lockHeld);
    await drawQueue();
    for (;;) {
      const taking = await queueTakeFirst();
      if (!taking) break;
      // не взяли — значит эту запись прямо сейчас посылает другая вкладка:
      // ждём её и не лезем вперёд, порядок постановки важнее скорости
      if (!taking.taken) break;
      const record = taking.record;
      await drawQueue();
      const heartbeat = setInterval(() => { renewLease(record.id); }, HEARTBEAT_MS);
      let outcome;
      try { outcome = await queueSendRecord(record); } finally { clearInterval(heartbeat); }
      if (outcome.sent) {
        recentlySent.push({ ...record, sentAt: new Date().toISOString() });
        recentlySent = recentlySent.slice(-5);
        setTimeout(drawQueue, 61000); // убрать подпись «ушло из очереди» через минуту
        await queueRemove(record.id);
        if (screenNow() === record.экран) openScreen(record.экран);
        await drawQueue();
        continue;
      }
      if (outcome.login) {
        // сессии больше нет: запись не выполнена и остаётся ждать, но вместе
        // с сессией уходит и она сама — чужой браузер не хранит написанное
        await queueSetStatus(record.id, 'ожидает');
        goToLogin();
        break;
      }
      if (outcome.doubtful) {
        await queueSetStatus(record.id, 'сомнительно');
        sema.banner('Связь оборвалась на полпути: не понятно, дошло ли. Сёма спрашивает в переписке — отправить ещё раз или убрать.');
        await drawQueue();
        break; // связь только что порвалась — следующей записи ловить нечего
      }
      if (outcome.refused) {
        // Отказ — не «ждёт связи»: повторять его каждые двадцать секунд
        // бессмысленно, а молчать о нём нечестно. Строка с причиной и
        // кнопкой «Повторить» стоит в переписке, остальная очередь идёт
        await queueSetStatus(record.id, 'отказ', outcome.refused);
        sema.banner('Отложенное не ушло: ' + outcome.refused);
        await drawQueue();
        continue;
      }
      await queueSetStatus(record.id, 'ожидает'); // связи нет — так и подписано
      await drawQueue();
      break; // не долбим сеть в цикле — следующая попытка по следующему сигналу о связи
    }
  };
  const runQueue = async () => {
    if (queueRunning) return;
    queueRunning = true;
    try { await underLock(queuePass); } finally { queueRunning = false; }
  };

  const queuePanel = () => {
    let p = document.getElementById('outbox');
    if (!p) {
      p = document.createElement('div');
      p.id = 'outbox'; p.className = 'outbox'; p.hidden = true;
    }
    return p;
  };
  const queueButton = (name, handler) => {
    const btn = document.createElement('button');
    btn.type = 'button'; btn.className = 'action'; btn.textContent = name;
    btn.addEventListener('click', handler);
    return btn;
  };
  const queueRow = (text, timeText, buttons, cls) => {
    const row = document.createElement('div');
    row.className = 'queue-item' + (cls ? ' ' + cls : '');
    const touch = document.createElement('span'); touch.className = 'queue-text'; touch.textContent = text;
    const v = document.createElement('span'); v.className = 'queue-time'; v.textContent = timeText;
    row.append(touch, v);
    (buttons || []).forEach((ctl) => row.append(ctl));
    return row;
  };
  const drawQueue = async () => {
    const bottom = document.querySelector('.bottom');
    const composerForm = document.querySelector('form.composer');
    const p = queuePanel();
    if (!bottom || !composerForm) { p.hidden = true; return; }
    if (!p.isConnected) bottom.insertBefore(p, composerForm);
    const screen = screenNow();
    const all = (await queueList()).filter((rec) => rec.экран === screen);
    recentlySent = recentlySent.filter((rec) => Date.now() - Date.parse(rec.sentAt) < 60000);
    const sentHere = recentlySent.filter((rec) => rec.экран === screen);
    p.innerHTML = '';
    all.forEach((rec) => {
      const text = rec.вид === 'файл' ? 'Файл «' + rec.имяФайла + '»' : rec.текст;
      if (rec.статус === 'сомнительно') {
        p.append(queueRow(text, 'не понятно, ушло ли — связь оборвалась на полпути', [
          queueButton('Отправить всё равно', async () => { await queueSetStatus(rec.id, 'ожидает'); runQueue(); }),
          queueButton('Убрать', async () => { await queueRemove(rec.id); await drawQueue(); }),
        ], 'doubtful'));
        return;
      }
      if (rec.статус === 'отказ') {
        // причина названа прямо в строке: без неё человек видел «ждёт
        // связи» при живой связи и ждал бы вечно
        p.append(queueRow(text, rec.ошибка || 'Сёма не принял это', [
          queueButton('Повторить', async () => { await queueSetStatus(rec.id, 'ожидает'); runQueue(); }),
          queueButton('Убрать', async () => { await queueRemove(rec.id); await drawQueue(); }),
        ], 'refused'));
        return;
      }
      const timeText = rec.статус === 'уходит' ? 'уходит…' : 'написано в ' + shortTime(rec.поставлено) + ', ждёт связи';
      const buttons = rec.статус === 'уходит' ? [] : [queueButton('Отменить', async () => { await queueRemove(rec.id); await drawQueue(); })];
      p.append(queueRow(text, timeText, buttons));
    });
    sentHere.forEach((rec) => {
      const text = rec.вид === 'файл' ? 'Файл «' + rec.имяФайла + '»' : rec.текст;
      p.append(queueRow(text, 'ушло из очереди · написано в ' + shortTime(rec.поставлено), [], 'sent'));
    });
    p.hidden = all.length === 0 && sentHere.length === 0;
  };
  document.body.addEventListener('htmx:after:swap', () => { drawQueue(); });

  // ---- постановка в очередь: композер и скрепка ----
  //
  // Написанное забирается из элемента сразу и целиком (`собрать…`), а уже
  // потом решается, каким состоянием оно ложится: между этими двумя делами
  // стоит проба связи, и за её время человек успел бы набрать в поле
  // следующую строку — забрали бы её вместо той, что отправляли.
  const takeMessage = (form) => {
    const field = form.querySelector('textarea');
    if (!field || !field.value.trim()) return null;
    let values = {};
    try { values = JSON.parse(form.getAttribute('hx-vals') || '{}'); } catch (e) {}
    const screen = screenNow();
    const record = {
      вид: 'сообщение', действие: values.action || 'send_message', цель: values.target || '',
      экран: screen, поле: field.name || 'text', текст: field.value,
    };
    field.value = '';
    sema.field(field);
    drafts.save(screen, '');
    return record;
  };
  const takeFile = (node) => {
    const file = node.files && node.files[0];
    node.value = '';
    if (!file) return null;
    return { вид: 'файл', действие: 'attach_file', цель: '', экран: screenNow(), имяФайла: file.name, файл: file };
  };
  const enqueue = async (record, status) => {
    if (!record) return;
    await queueAdd(record, status);
    const what = record.вид === 'файл' ? 'файл' : 'сообщение';
    if (status === 'сомнительно') {
      sema.banner('Связь оборвалась на полпути: не понятно, ушло ли ' + what + '. Сёма спрашивает в переписке — отправить ещё раз или убрать.');
    } else {
      sema.banner('Нет связи — ' + what + ' подождёт в очереди и уйдёт, когда она появится.');
    }
    await drawQueue();
    runQueue(); // вдруг связь уже вернулась, а мы об этом ещё не знали
  };
  const queueMessage = (form, status) => enqueue(takeMessage(form), status);
  const queueFile = (node, status) => enqueue(takeFile(node), status);

  // submit/change перехватываем на стадии захвата — раньше, чем до них
  // доберётся сам htmx (тот же приём, что у data-confirm выше по
  // файлу): событие останавливается совсем, и htmx:before:request для
  // этой отправки не наступает вовсе
  const deferrable = (node) => !!(node && node.hasAttribute && node.hasAttribute('data-deferrable'));
  document.body.addEventListener('submit', (evt) => {
    const form = evt.target;
    if (!deferrable(form) || !offlineNow()) return;
    evt.preventDefault();
    evt.stopImmediatePropagation();
    queueMessage(form);
  }, true);
  document.body.addEventListener('change', (evt) => {
    const node = evt.target;
    if (!node || node.type !== 'file' || !deferrable(node) || !offlineNow()) return;
    evt.preventDefault();
    evt.stopImmediatePropagation();
    queueFile(node);
  }, true);

  // Связь казалась живой (сеть === true), а запрос всё равно не дошёл:
  // htmx:error без ответа — сетевое исключение, а не отказ сервера (тот
  // приходит через htmx:response:error и здесь не в счёт). Композер и
  // скрепка попадают в очередь и отсюда, а не теряются молча.
  //
  // Каким состоянием — решает проба: этот запрос уже ушёл в сеть, и «Сёма
  // не отвечает вовсе» надо отличить от «оборвалось на полпути». Не
  // отвечает — до него не дошло, запись просто ждёт связи; отвечает —
  // значит могло и выполниться, и повтор дал бы второе сообщение.
  document.body.addEventListener('htmx:error', (evt) => {
    const ctx = evt.detail && evt.detail.ctx;
    if (ctx && ctx.response) return;
    online = false;
    const node = ctx && ctx.sourceElement;
    if (!deferrable(node)) return;
    const isFile = node.tagName === 'INPUT' && node.type === 'file';
    const record = isFile ? takeFile(node) : takeMessage(node);
    if (!record) return;
    linkAlive().then((alive) => {
      online = alive;
      return enqueue(record, alive ? 'сомнительно' : 'ожидает');
    }, () => enqueue(record, 'ожидает'));
  });
  document.body.addEventListener('htmx:response:error', () => { online = true; }); // ответ есть — связь есть, пусть и с отказом
  document.body.addEventListener('htmx:after:request', (evt) => {
    const ctx = evt.detail && evt.detail.ctx;
    if (ctx && ctx.response && ctx.response.status >= 200 && ctx.response.status < 300) { online = true; runQueue(); }
  });

  // Неоткладываемое действие без связи не уходит вовсе, а честно говорит
  // почему: подтверждение команды, перенос в спам, приём отпечатка,
  // права, удаление — решения этого мгновения, повтор через восемь
  // часов хуже отсутствия (design offline-snapshot, решение 7). Композер
  // и скрепка сюда не доходят — их submit/change остановлены раньше, до
  // этого события, и preventDefault здесь — тот же приём, которым htmx
  // сам умеет отменять исходящий запрос (проверено по исходнику htmx.js:
  // trigger() возвращает false, когда кто-то вызвал preventDefault, и
  // это возвращает вызывающий код до e.fetch(...)).
  document.body.addEventListener('htmx:before:request', (evt) => {
    const ctx = evt.detail && evt.detail.ctx;
    const req = ctx && ctx.request;
    if (!req || (req.method || '').toLowerCase() !== 'post' || !offlineNow()) return;
    evt.preventDefault();
    const node = ctx.sourceElement;
    if (pressable(node)) node.classList.remove('busy');
    sema.banner('Нет связи с Сёмой — для этого действия нужна связь. Попробуйте, когда она появится.');
  });

  window.addEventListener('online', () => { online = true; runQueue(); });
  window.addEventListener('offline', () => { online = false; drawQueue(); });
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') runQueue(); });
  setInterval(() => { if (online) runQueue(); }, 20000);

  // Старт: запись, оставшаяся «уходит» с прошлой загрузки ЭТОЙ вкладки, —
  // это запрос, оборванный на середине (страница перезагрузилась, пока
  // он летел). Самое важное здесь — не отправить дважды: неизвестность
  // честнее угадывания, и такая запись не уходит по кругу сама, а ждёт
  // решения человека («Отправить всё равно» или «Убрать»).
  //
  // Чужие записи здесь не трогаем вовсе. Раньше трогали, и вторая вкладка,
  // открытая ровно в тот миг, когда первая честно посылала сообщение,
  // объявляла эту отправку сомнительной — а человек нажимал «Отправить всё
  // равно» и получал дубль. Кто из отправителей ещё жив, умеет отличать
  // только освободитьБрошенные: у неё есть замок браузера и аренда по
  // времени, а у загрузки страницы — ничего, кроме догадки.
  (async () => {
    for (const rec of await queueList()) {
      if (rec.статус === 'уходит' && rec.вкладка === myTab) await queueSetStatus(rec.id, 'сомнительно');
    }
    await drawQueue();
    runQueue();
  })();
})();

// Адрес экрана живёт в хэше: обновление страницы, «назад» и ссылка на экран
// работают, а запрос к /ui остаётся запросом к куску, не к странице.
const screenFromHash = () => (new URLSearchParams(location.hash.replace(/^#/, '')).get('screen') || '');
// Заголовки наших запросов ставим здесь — и до первого запроса
// оболочки, а не после. Раньше этот блок стоял в конце файла, и самый
// первый запрос уходил без заголовков: сервер не знал ни текущего
// экрана, ни того, что окно узкое, и на телефоне «Чаты» открывались
// последним разговором вместо списка.
document.body.addEventListener('htmx:before:request', (evt) => {
  const sect = document.getElementById('screen');
  const req = evt.detail && evt.detail.ctx && evt.detail.ctx.request;
  if (!req || !req.headers) return;
  if (sect && sect.dataset.screen) {
    // кодируем: в адресе экрана бывает кириллица («…~log:хозяин»), а
    // заголовок HTTP терпит только ISO-8859-1 — иначе браузер роняет
    // запрос сам, до сервера, и вся навигация встаёт
    req.headers['HX-Current-Screen'] = encodeURIComponent(sect.dataset.screen);
    // где человек стоит в списке: отбор, поиск и порция. Действие на этом же
    // экране настроек пересобирает его таким же (сервер разбирает заголовки сам и
    // только для настроек): нашёл чат поиском, нажал клетку в матрице прав —
    // поиск не пропадает, а строка остаётся на месте
    if (sect.dataset.filter) req.headers['X-Sema-Filter'] = encodeURIComponent(sect.dataset.filter);
    if (sect.dataset.limit) req.headers['X-Sema-Limit'] = sect.dataset.limit;
    const typed = document.getElementById('search');
    if (typed && typed.value) req.headers['X-Sema-Search'] = encodeURIComponent(typed.value);
  }
  // ширина окна — не украшение: на телефоне «Чаты» открывают список, а
  // не последний разговор, и решает это сервер, а не стиль. Знать её он
  // может только от нас
  if (phone()) req.headers['X-Sema-Phone'] = '1';
});
const phone = () => window.matchMedia('(max-width: 720px)').matches;
// повернули телефон или растянули окно — экран пересобирается: с той
// стороны это другой экран, а не другая ширина
(() => {
  let wasPhone = phone();
  window.addEventListener('resize', () => {
    if (phone() === wasPhone) return;
    wasPhone = phone();
    // подогретое собрано для прежней раскладки
    if (sema.prefetchDrop) sema.prefetchDrop();
    const el = document.getElementById('screen');
    if (!el) return;
    // у настроек на узком и на широком экране разные документы целиком —
    // колонка разделов и поиск в ней или «‹ Настройки» над заголовком, —
    // а перечитывание экрана подменяет только ленту. Поэтому окно
    // перечитывается целиком; несохранённая правка такого права не даёт
    if (/^settings(:|$)/.test(screenNow()) && !(sema.unsaved && sema.unsaved())) {
      openScreen(screenNow(), 0, null, true);
      return;
    }
    htmx.trigger(el, 'sema:refresh');
  });
})();


// Первый запрос делает не htmx по hx-trigger="load", а мы сами: иначе
// гонка — htmx успевал запросить /ui без экрана раньше, чем адрес из хэша
// попадал в hx-get, и вместо запрошенного экрана открывалась «Почта».
//
// И повторяем его, пока экран не приедет. Раньше запрос был ровно один:
// не дошёл — оболочка навсегда оставалась с надписью «Подключаюсь к
// Сёме…», хотя тут же обещала, что попробует снова. На телефоне это
// случалось после каждого выката: приложение просыпается из фона,
// оболочку отдаёт служебный работник из кэша, а сервер в этот момент
// поднимается — и приложение приходилось выгружать из памяти.
//
// Признак «приехало» — появился ли #экран, а не то, что сказал htmx:
// экран на месте, значит спрашивать больше нечего, чем бы ни кончился
// сам запрос.
// Заставка (#splash в index.html) стоит, пока первый экран не приехал: до
// этого окно пустое, и прежде человек смотрел на белый лист. Её подписи
// «подключаюсь» и «связь медленная» идут сами по времени, а сказать
// своё — «не отвечает» — может только этот блок.
const splashSay = (text) => {
  const box = document.getElementById('splash');
  const said = box && box.querySelector('.said');
  if (!said) return;
  box.classList.add('told');
  said.textContent = text;
};
const splashOff = () => {
  const box = document.getElementById('splash');
  if (!box || box.classList.contains('gone')) return;
  box.classList.add('gone');
  setTimeout(() => box.remove(), 200);
};
document.body.addEventListener('htmx:after:swap', () => { if (document.getElementById('screen')) splashOff(); });
(() => {
  const win = document.getElementById('window');
  if (!win) return;
  let pause = 1000, inFlight = false, attempts = 0, guard = null;
  const stillWaiting = () => !document.getElementById('screen');
  // первый экран уже просил встроенный скрипт index.html (perf-pass 6.4):
  // его ответ отдаётся первому такому же запросу вместо второго хода в сеть
  document.body.addEventListener('htmx:before:request', (evt) => {
    const first = window.semaFirstScreen, ctx = evt.detail && evt.detail.ctx;
    if (!first || !ctx || !ctx.request) return;
    window.semaFirstScreen = null; // одноразовый: повтор после отказа идёт в сеть
    if (ctx.request.action === first.url) ctx.fetch = () => first.resp;
  });
  const callShell = () => {
    if (inFlight || !stillWaiting()) return;
    inFlight = true;
    attempts++;
    const wanted = screenFromHash();
    htmx.ajax('GET', wanted ? '/ui?screen=' + encodeURIComponent(wanted) : '/ui', { source: win, target: '#window', swap: 'innerHTML' });
    // страховка на случай, если о конце запроса нам не скажут вовсе
    clearTimeout(guard);
    guard = setTimeout(() => { inFlight = false; again(); }, 20000);
  };
  const again = () => {
    if (!stillWaiting()) return;
    // htmx 4 подставляет тело отказа в цель как обычный ответ, и в окне
    // вместо надписи оказывается «502 Bad Gateway» от прокси. Пишем
    // своё сразу после неудачи, а не перед следующей попыткой: между
    // ними проходят секунды, и всё это время человек читал бы отказ
    win.innerHTML = '<div class="connecting">Сёма не отвечает — пробую снова…</div>';
    splashSay('Сёма не отвечает — пробую снова…');
    pause = Math.min(pause * 2, 15000);
    setTimeout(callShell, pause);
  };
  // htmx 4 подставляет тело отказа в цель как обычный ответ: пока экрана
  // нет, в окне вместо нашей надписи оказывался «502 Bad Gateway» от
  // прокси. Подмену отменяем — окно наше, пока первый экран не приехал
  // конец любого запроса, пока экрана нет, — это конец нашего
  document.body.addEventListener('htmx:finally:request', () => {
    if (!stillWaiting()) return;
    clearTimeout(guard);
    inFlight = false;
    again();
  });
  // сеть вернулась или приложение вернулось из фона — не ждём паузу
  const atOnce = () => { pause = 1000; inFlight = false; callShell(); };
  window.addEventListener('online', atOnce);
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible' && stillWaiting()) atOnce(); });
  callShell();
})();
window.addEventListener('popstate', () => {
  const wanted = screenFromHash();
  if (wanted && wanted !== screenNow()) openScreen(wanted);
});

// Боковые панели на телефоне: одна точка входа, она же ставит замок
// прокрутки. Без замка палец над открытым меню прокручивал не меню, а
// содержимое под ним — меню стояло, а «подложка» ехала.
sema.panel = (name, openWire) => {
  const node = document.getElementById(name);
  if (!node) return;
  const was = node.classList.contains('open-sheet');
  const want = openWire === undefined ? !was : !!openWire;
  node.classList.toggle('open-sheet', want);
  // закрыли шторку — поиск по разделам стирается: открывший её заново
  // ищет заново, а не разбирается, почему видно три пункта из шестнадцати
  if (name === 'menu' && !want && menuQuery) sema.searchMenu('');
  // открыли — подводим к тому разделу, где стоим: на телефоне шестнадцать
  // разделов в шторку не помещаются, и «Ключи и секреты» оказывались за
  // краем, хотя человек в них и находится
  if (name === 'menu' && want) {
    const box = node.querySelector('.item.selected');
    if (box && box.scrollIntoView) box.scrollIntoView({ block: 'center' });
  }
  if (want) {
    for (const other of ['menu', 'panel']) {
      if (other !== name) {
        const det = document.getElementById(other);
        if (det) det.classList.remove('open-sheet');
      }
    }
  }
  scrollLock();
  sheetAccessibility(node, name, want, was);
};

// ---- шторка с клавиатуры и для чтения вслух ----
//
// Шторка — это вопрос «в какой раздел», заданный поверх экрана, и вести
// себя она обязана как вопрос. Мышью это уже работало, а с клавиатуры и
// со скринридером — нет: Escape не закрывал, фокус уходил за шторку на
// страницу под ней, а «назад» возвращаться было некуда. Человек, который
// не пользуется мышью, открыв шторку, оказывался в ловушке.
let openedFrom = null;

const focusable = (node) => Array.from(node.querySelectorAll(
	'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
)).filter((el) => el.offsetParent !== null);

const sheetAccessibility = (node, name, want, was) => {
	// пока шторка открыта, страница под ней для чтения вслух не
	// существует: иначе скринридер спокойно уходит за затемнение и
	// читает то, чего человек не видит
	const underlay = document.getElementById('window');
	if (want) {
		node.setAttribute('role', 'dialog');
		node.setAttribute('aria-modal', 'true');
		if (!node.getAttribute('aria-label')) {
			node.setAttribute('aria-label', name === 'menu' ? 'Меню' : 'Разделы настройки');
		}
		if (underlay) underlay.setAttribute('aria-hidden', 'true');
		if (!was) openedFrom = document.activeElement;
		const first = focusable(node)[0];
		if (first) first.focus();
	} else {
		node.removeAttribute('aria-modal');
		if (underlay) underlay.removeAttribute('aria-hidden');
		// фокус возвращаем туда, откуда пришли: иначе он падает в начало
		// страницы, и человек заново идёт табом через всё меню
		if (was && openedFrom && openedFrom.focus) openedFrom.focus();
		openedFrom = null;
	}
	// кнопка, открывающая шторку, говорит о её состоянии
	document.querySelectorAll('[data-panel="' + name + '"]').forEach((ctl) => {
		ctl.setAttribute('aria-expanded', want ? 'true' : 'false');
	});
};

document.addEventListener('keydown', (evt) => {
	const openSheet = ['menu', 'panel']
		.map((nm) => document.getElementById(nm))
		.find((el) => el && el.classList.contains('open-sheet'));
	if (!openSheet) return;
	if (evt.key === 'Escape') {
		evt.preventDefault();
		sema.panel(openSheet.id, false);
		return;
	}
	if (evt.key !== 'Tab') return;
	// фокус ходит по кругу внутри шторки: уйти табом на страницу под
	// затемнением значит потерять и шторку, и место, куда вернуться
	const picker = focusable(openSheet);
	if (picker.length === 0) return;
	const first = picker[0];
	const last = picker[picker.length - 1];
	if (!evt.shiftKey && document.activeElement === last) {
		evt.preventDefault();
		first.focus();
	} else if (evt.shiftKey && document.activeElement === first) {
		evt.preventDefault();
		last.focus();
	}
});

// Затемнение под шторкой — отдельный узел со своим слушателем. Было
// ::before у body, и касание мимо меню попадало в сам body, а Safari на
// iPhone по касанию «некликабельного» места click не шлёт вовсе: открытое
// меню на телефоне не закрывалось ничем, кроме свайпа (4 октября 2026).
// Слушатель на самом узле делает его нажимаемым для iOS; закрывает он
// обе шторки — под затемнением другой не бывает.
const dimOverlay = () => {
  let dim = document.getElementById('dim-overlay');
  if (!dim) {
    dim = document.createElement('div');
    dim.id = 'dim-overlay';
    dim.setAttribute('aria-hidden', 'true');
    dim.addEventListener('click', () => {
      ['menu', 'panel'].forEach((nm) => sema.panel(nm, false));
    });
    document.body.appendChild(dim);
  }
  return dim;
};

const scrollLock = () => {
  const sheetOpen = ['menu', 'panel'].some((nm) => {
    const el = document.getElementById(nm);
    return el && el.classList.contains('open-sheet');
  });
  if (sheetOpen) dimOverlay();
  document.body.classList.toggle('panel-open', sheetOpen);
};

// боковые панели на телефоне закрываются по нажатию мимо них. Кнопка,
// которая панель открывает, из этого правила исключена: её клик всплывает
// сюда сразу после открытия и закрывал бы шторку тем же нажатием. Раньше
// исключение было по классу «бургер», а кнопка «Меню» в нижней полосе —
// «вкладка»: на телефоне меню не открывалось вовсе, и «Задачи», «Память»,
// «Здоровье» и настройки были недоступны. Признак теперь у самой кнопки
// (data-panel), а не у её оформления.
document.body.addEventListener('click', (evt) => {
  const handle = evt.target.closest('[data-panel]');
  for (const name of ['menu', 'panel']) {
    const node = document.getElementById(name);
    if (node && node.classList.contains('open-sheet') && !node.contains(evt.target) &&
        !(handle && handle.getAttribute('data-panel') === name)) {
      sema.panel(name, false);
    }
  }
});

// Жесты панелей. У меню он вертикальный: меню приезжает снизу шторкой,
// и тянуть её вбок было бы враньём про то, куда она уедет. Свайп вниз по
// шторке закрывает; открывает её кнопка «Меню» на полосе — жест от
// нижнего края занят системой (домой, переключение приложений).
//
// Панель экрана (список чатов, список писем) по-прежнему слева: свайп от
// левого края открывает, влево по открытой — закрывает.
//
// Свайп вниз закрывает шторку, только когда её список стоит в самом верху
// или палец взялся за закреплённый верх (ручка, шапка, поиск). Иначе это прокрутка: 4 октября 2026
// владелец листал пункты меню на телефоне, тянул список обратно наверх — и
// меню закрывалось под пальцем (как у шторок iOS: сначала доедь до верха).
(() => {
  let sx = 0, sy = 0, st = 0, from = '', sheetAtTop = true;
  const edge = 28, threshold = 60, slope = 1.4, span = 700;
  const sheetOpen = (name) => {
    const el = document.getElementById(name);
    return !!el && el.classList.contains('open-sheet');
  };
  document.addEventListener('touchstart', (ev) => {
    if (ev.touches.length !== 1) { from = ''; return; }
    const touch = ev.touches[0];
    sx = touch.clientX; sy = touch.clientY; st = Date.now();
    from = sheetOpen('menu') ? 'menu' : sheetOpen('panel') ? 'panel' : (sx <= edge ? 'edge' : '');
    if (from === 'menu') {
      const menu = document.getElementById('menu');
      // ручка — весь закреплённый верх шторки: при прокрутке ручка, шапка и
      // поиск встают в одну точку, и под пальцем оказывается поиск
      const handle = ev.target && ev.target.closest && ev.target.closest('#menu .grabber, #menu .menu-header, #menu .menu-search');
      sheetAtTop = !!handle || menu.scrollTop <= 0;
    }
  }, { passive: true });
  document.addEventListener('touchend', (ev) => {
    if (!from || Date.now() - st > span) { from = ''; return; }
    const touch = (ev.changedTouches && ev.changedTouches[0]) || null;
    if (!touch) { from = ''; return; }
    const dx = touch.clientX - sx, dy = touch.clientY - sy;
    const was = from;
    from = '';
    if (was === 'menu') {
      // шторка: тянут вниз, и движение должно быть заметно вертикальным, а
      // список — стоять в верху с начала жеста (см. выше)
      if (sheetAtTop && dy > threshold && dy > Math.abs(dx) * slope) sema.panel('menu', false);
      return;
    }
    if (Math.abs(dx) < threshold || Math.abs(dx) < Math.abs(dy) * slope) return;
    if (was === 'edge' && dx > 0) sema.panel('panel', true);
    else if (was === 'panel' && dx < 0) sema.panel('panel', false);
  }, { passive: true });
})();

// ---- Жест «потянуть вниз» на телефоне (phone-refresh) ----
//
// Жест — третий ПОВОД обновить экран, но не третья дорога: он дёргает то же
// событие «sema:refresh» на #экран, которым пользуются живой провод и
// редкий опрос. Своего запроса здесь нет нарочно — иначе в клиенте появился
// бы путь обновления, которым никто не ходит и который никто не проверяет:
// чем это кончается, в проекте записано (сжатие годами стояло на ручке
// снятого клиента).
//
// Величины согласованы с владельцем 27 сентября 2026 по макету
// design/обновление-телефон: жест замечен с 10 точек, срабатывает на 60 —
// то же число, что у бокового свайпа выше по файлу, чтобы у пальца не было
// двух разных «дошёл — отпускай», — дальше 80 указатель не едет, «Обновлено»
// держится 600 мс, «Не получилось» 1200 мс. Наклон взят у того же свайпа:
// вбок — панели, вниз — обновлению.
(() => {
  const START_PX = 10, THRESHOLD_PX = 60, MAX_PX = 80, SLOPE = 1.4;
  const DONE_MS = 600, FAIL_MS = 1200, DEADLINE_MS = 20000;

  const ARROW_SVG = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M8 2.5v9M4 8l4 4 4-4"/></svg>';
  const CIRCLE_SVG = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><path d="M14 8a6 6 0 1 1-1.8-4.3"/></svg>';
  const CHECK_SVG = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 8.5l3.2 3L13 4.5"/></svg>';
  const TROUBLE_SVG = '<svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="8" cy="8" r="6"/><path d="M8 5.2v3.6"/><path d="M8 10.8h.01"/></svg>';
  const SIGNS = { noticed: ARROW_SVG, threshold: ARROW_SVG, running: CIRCLE_SVG, done: CHECK_SVG, failed: TROUBLE_SVG };

  let sx = 0, sy = 0, pulling = false, eligible = false, refreshing = false, unbind = null;
  // Поколение указателя: «Обновлено» сворачивается через 600 мс, и человек
  // успевает потянуть снова раньше этого срока. Без счёта отложенное
  // сворачивание убирало бы уже НОВЫЙ указатель — жест начался, а знака нет.
  let generation = 0;
  const removeLater = (ms) => {
    const mineGen = ++generation;
    setTimeout(() => { if (generation === mineGen) removeIndicator(); }, ms);
  };

  const sheetOpen = (name) => {
    const el = document.getElementById(name);
    return !!el && el.classList.contains('open-sheet');
  };
  const feed = () => document.getElementById('feed');

  // Указатель живёт в #экране рядом с лентой, а не внутри неё: перечитывание
  // подменяет ровно #лента (hx-target), и указатель внутри исчез бы в тот
  // самый миг, когда должен сказать «обновлено».
  const indicator = () => {
    const sect = document.getElementById('screen');
    const feedEl = feed();
    if (!sect || !feedEl) return null;
    let u = document.getElementById('pull-refresh');
    if (!u) {
      u = document.createElement('div');
      u.id = 'pull-refresh';
      u.className = 'pull-refresh';
      u.setAttribute('role', 'status');
    }
    if (u.nextSibling !== feedEl) sect.insertBefore(u, feedEl);
    return u;
  };
  const removeIndicator = () => {
    const u = document.getElementById('pull-refresh');
    if (!u) return;
    u.remove();
  };
  // Вид указателя — четыре состояния макета. Высота едет за пальцем, пока он
  // на экране: без движения человек не понимает, принят жест или нет.
  // Разметка пересобирается только при смене состояния — иначе знак мигал бы
  // на каждом кадре движения. Признак вида читаем через getAttribute:
  // кириллические data-атрибуты в dataset не попадают вовсе.
  const look = (state, text, height) => {
    const u = indicator();
    if (!u) return;
    if (u.getAttribute('data-view') !== state) {
      u.setAttribute('data-view', state);
      u.className = 'pull-refresh ' + state;
      u.innerHTML = '<span class="sign">' + SIGNS[state] + '</span><span class="word"></span>';
    }
    const word = u.querySelector('.word');
    if (word && word.textContent !== text) word.textContent = text;
    u.style.height = Math.round(height) + 'px';
  };

  // Итог виден так же прямо, как удача: молчание после жеста неотличимо от
  // «ничего не изменилось», и человек тянет снова, не понимая, что случилось.
  const outcome = (ok2, reason) => {
    if (!refreshing) return;
    refreshing = false;
    if (unbind) { unbind(); unbind = null; }
    if (ok2) {
      look('done', 'Обновлено', 34);
      removeLater(DONE_MS);
      return;
    }
    look('failed', 'Не получилось: ' + (reason || 'нет связи'), 40);
    removeLater(FAIL_MS);
  };
  const startRefresh = () => {
    // пока обновление идёт, повторный жест второго запроса не заводит — а
    // человеку при этом видно, что идёт первое
    if (refreshing) return;
    const el = document.getElementById('screen');
    if (!el) { removeIndicator(); return; }
    refreshing = true;
    // на экране из снимка жест — не «перечитать», а «попробовать связаться
    // снова»: отдельной кнопки повтора для этого не заводится (phone-refresh
    // 5.1, закрывает открытый вопрос offline-snapshot)
    look('running', snapshotAt ? 'Пробую связаться…' : 'Обновляю…', 56);
    const onSwapOk = () => {
      // Ответ из снимка — это не «обновлено»: служебный работник отдал
      // вчерашнее, и галочка тут была бы неправдой. Шапку снимка при этом не
      // трогаем — её ставит и снимает показатьСнимок (phone-refresh 5.2).
      if (snapshotAt) outcome(false, 'нет связи');
      else outcome(true);
    };
    const netTrouble = (ev) => {
      const ctx = ev.detail && ev.detail.ctx;
      if (ctx && ctx.response) return; // отказ сервера придёт другим событием
      outcome(false, 'нет связи');
    };
    const replyTrouble = (ev) => {
      const o = ev.detail && ev.detail.ctx && ev.detail.ctx.response;
      outcome(false, o && o.status ? 'Сёма ответил ' + o.status : 'Сёма не ответил');
    };
    const guard = setTimeout(() => outcome(false, 'Сёма не ответил'), DEADLINE_MS);
    unbind = () => {
      clearTimeout(guard);
      document.body.removeEventListener('htmx:after:swap', onSwapOk);
      document.body.removeEventListener('htmx:error', netTrouble);
      document.body.removeEventListener('htmx:response:error', replyTrouble);
    };
    document.body.addEventListener('htmx:after:swap', onSwapOk);
    document.body.addEventListener('htmx:error', netTrouble);
    document.body.addEventListener('htmx:response:error', replyTrouble);
    htmx.trigger(el, 'sema:refresh');
  };

  // Жест живёт только на телефоне и только когда прокручивать вверх нечего,
  // и только в том блоке, где палец. Это же правило снимает спор с
  // прокруткой ленты: посреди переписки то же движение вниз остаётся
  // обычной прокруткой к более старым сообщениям, а жест не начинается вовсе.
  // При открытой шторке меню или боковой панели жест выключен: там человек
  // занят другим.
  const allowed = (target) => {
    if (!phone() || refreshing) return false;
    if (sheetOpen('menu') || sheetOpen('panel')) return false;
    const feedEl = feed();
    if (!feedEl || !target || !feedEl.contains(target)) return false;
    return feedEl.scrollTop <= 0;
  };
  document.addEventListener('touchstart', (ev) => {
    pulling = false; eligible = false;
    if (ev.touches.length !== 1 || !allowed(ev.target)) return;
    sx = ev.touches[0].clientX; sy = ev.touches[0].clientY;
    eligible = true;
    generation++; // отложенное сворачивание прошлого указателя нас больше не касается
  }, { passive: true });
  // Слушатель нарочно не passive: пока идёт наш жест, ехать должен указатель,
  // а не лента под ним (родную резинку внутренней прокрутки гасит только
  // preventDefault; родное потягивание страницы погашено раньше, в стилях).
  // Цена — браузер ждёт этот обработчик перед прокруткой, поэтому первым
  // делом здесь стоит дешёвый выход: пока жест не начат, работы ровно
  // на два чтения.
  document.addEventListener('touchmove', (ev) => {
    if (!eligible || ev.touches.length !== 1) return;
    const dx = ev.touches[0].clientX - sx, dy = ev.touches[0].clientY - sy;
    if (!pulling) {
      // вбок — не наше: горизонтальное движение отдаётся боковой панели по
      // тому же порогу наклона, что у свайпа от края
      if (Math.abs(dx) > START_PX && Math.abs(dx) >= dy) { eligible = false; return; }
      if (dy < START_PX || dy < Math.abs(dx) * SLOPE) return;
      pulling = true;
    }
    const feedEl = feed();
    if (!feedEl || feedEl.scrollTop > 0) { eligible = false; pulling = false; removeIndicator(); return; }
    if (ev.cancelable) ev.preventDefault();
    const reached = dy >= THRESHOLD_PX;
    look(reached ? 'threshold' : 'noticed',
      reached ? 'Отпустите — обновим' : 'Потяните для обновления',
      Math.min(dy, MAX_PX));
  }, { passive: false });
  document.addEventListener('touchend', (ev) => {
    const was = pulling && eligible;
    eligible = false; pulling = false;
    if (!was) return;
    const touch = (ev.changedTouches && ev.changedTouches[0]) || null;
    // отпустили раньше порога — не случилось ничего, и это видно: указатель
    // просто уезжает, экран остаётся прежним
    if (!touch || touch.clientY - sy < THRESHOLD_PX) { removeIndicator(); return; }
    startRefresh();
  }, { passive: true });
  document.addEventListener('touchcancel', () => {
    eligible = false; pulling = false;
    if (!refreshing) removeIndicator();
  }, { passive: true });
})();

// Поиск по разделам в шторке. Ищем на месте, без похода на сервер: иначе
// каждая набранная буква — запрос, а на телефоне это ещё и по сети.
// Слова, по которым раздел находится («пуши», «ключи», «наружу»),
// приезжают с сервера атрибутом data-words: справочник синонимов там же,
// где обзор настроек, и двух списков не заводим.
let menuQuery = '';
sema.searchMenu = (text) => {
  const menu = document.getElementById('menu');
  if (!menu) return;
  menuQuery = text;
  const field = document.getElementById('menu-search');
  if (field && field.value !== text) field.value = text;
  const words = text.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const wasSearching = menu.classList.contains('searching');
  menu.classList.toggle('searching', words.length > 0);
  // поиск стёрли — узлы возвращаются к тому, что решил сервер по адресу
  // экрана: «раскрыт тот, где стоишь»
  if (wasSearching && words.length === 0) {
    menu.querySelectorAll('.menu-node').forEach((nodeEl) => {
      nodeEl.open = nodeEl.querySelector('summary.selected') !== null || nodeEl.querySelector('.item.selected') !== null;
    });
  }
  const fits = (node) => {
    const haystack = ((node.textContent || '') + ' ' + (node.getAttribute('data-words') || '')).toLowerCase();
    return words.every((s) => haystack.includes(s));
  };
  let found = 0;
  // сначала узлы: подошёл узел — показываем его целиком со всеми детьми
  menu.querySelectorAll('.menu-node').forEach((nodeEl) => {
    // пока ищем, узлы раскрыты: закрытый <details> прячет содержимое
    // не стилем, а сам, и найденное внутри «Настроек» было бы не видно.
    // Раскрытие по адресу экрана вернёт сервер при следующей перерисовке
    if (words.length > 0) nodeEl.open = true;
    const own = nodeEl.querySelector('summary');
    const whole = words.length === 0 || (own && fits(own));
    let visibleCount = 0;
    nodeEl.querySelectorAll('.well > .item').forEach((p) => {
      const visible = whole || fits(p);
      p.classList.toggle('hidden-by-search', !visible);
      if (visible) visibleCount++;
    });
    const visible = whole || visibleCount > 0;
    nodeEl.classList.toggle('hidden-by-search', !visible);
    if (visible) found++;
  });
  menu.querySelectorAll(':scope > .item').forEach((p) => {
    const visible = words.length === 0 || fits(p);
    p.classList.toggle('hidden-by-search', !visible);
    if (visible) found++;
  });
  menu.classList.toggle('menu-empty', words.length > 0 && found === 0);
};
// меню приезжает заново при каждой живой перерисовке — набранное
// возвращаем сами, иначе поиск стирается под руками
document.body.addEventListener('htmx:after:swap', () => {
  if (menuQuery) sema.searchMenu(menuQuery);
});

// после каждой подмены разметки: прокрутка вниз в переписке и высота поля
// обратный отсчёт тикает в браузере: сервер знает только момент
setInterval(() => {
  document.querySelectorAll('[data-until]').forEach((ctl) => {
    const secsLeft = Math.max(0, Math.round((new Date(ctl.dataset.until) - Date.now()) / 1000));
    ctl.textContent = secsLeft > 0 ? 'осталось ' + Math.floor(secsLeft / 60) + ':' + String(secsLeft % 60).padStart(2, '0') : 'сейчас';
    ctl.classList.toggle('ok', secsLeft === 0);
    ctl.classList.toggle('info', secsLeft > 0);
  });
}, 1000);

// строка пушей показывает, что с этим устройством: сервер этого не знает —
// подписка живёт в браузере, и мешать ей может система (режим окна, запрет)
document.body.addEventListener('htmx:after:swap', () => {
  // предложение: класс на body держит строку открытой между перерисовками;
  // здесь он только снимается, если открытым его оставила удачная подписка
  // («Готово» живёт до следующей отрисовки экрана)
  sema.offerApply(sema._offerOpen && !sema.offerDismissed());
  if (document.querySelector('[data-action="$push"]')) sema.pushPaint().catch(() => {});
});

// раскрытые <details> с id, открытые панели и прокрутка ленты переживают
// подмену: до запроса запоминаем, после подмены возвращаем. В переписке
// прокруткой ведает низ(), в списках — человек: пришло письмо, а он читал
// внизу списка — список не должен уезжать наверх
// Раскрытые помнятся по самому событию toggle, а не снимком перед
// запросом: снимок делался и перед запросом ленивой части, когда её тело
// ещё «Загружаю…», — и раскрытая строка канала закрывалась от каждого
// тумблера (запись экрана 13 сентября 2026). Узлы меню (id «м-…») сюда не
// берём: их раскрытие решает сервер по адресу экрана, иначе заглянувший в
// чужой раздел остаётся с ним раскрытым навсегда
const expandedIds = new Set();
document.body.addEventListener('toggle', (evt) => {
  const det = evt.target;
  if (!det || !det.id || det.tagName !== 'DETAILS' || det.id.startsWith('menu-') || !det.closest('#window')) return;
  if (det.open) expandedIds.add(det.id); else expandedIds.delete(det.id);
}, true);
let sheetsWere = [];
let feedWas = { screen: '', top: 0 };
// прокрутка на момент запроса экрана: после подмены ленивые части ещё
// пусты, лента коротка, и браузер сам ужимает scrollTop — «нынешняя»
// прокрутка после этого уже не та, что была у человека
let topAtRequest = { screen: '', top: 0 };
let screenWas = '';
document.body.addEventListener('htmx:before:request', (evt) => {
  // запрос самой части идёт от её тела; тумблер внутри части — это
  // запрос экрана, хоть и лежит в теле части
  const target = evt.target;
  const isFragment = !!(target && target.classList && target.classList.contains('fragment-body'));
  if (!isFragment) {
    const feed = document.getElementById('feed');
    topAtRequest = feed && !document.getElementById('input') ? { screen: screenNow(), top: feed.scrollTop } : { screen: '', top: 0 };
  }
  sheetsWere = ['menu', 'panel'].filter((nm) => { const el = document.getElementById(nm); return el && el.classList.contains('open-sheet'); });
  screenWas = screenNow();
  // откуда пришёл запрос: после подмены фокус вернётся в то же место
  focusWas = focusOrigin(document.activeElement);
  focusPending = true;
});
// прокрутку помним не с начала запроса, а нынешнюю: между запросом и
// ответом проходит десятая доля секунды, палец за это время уезжает на
// полсотни точек, и возврат «как было до запроса» отбрасывал список
// назад — это и был рывок. Следим за самим событием прокрутки, а не за
// подменой: порядок подмен (лента, потом меню) обещать нельзя
document.body.addEventListener('scroll', (ev) => {
  const feed = ev.target;
  if (!feed || feed.id !== 'feed') return;
  feedWas = document.getElementById('input') ? { screen: '', top: 0 } : { screen: screenNow(), top: feed.scrollTop };
}, true);
// ---- фокус после подмены ----
// htmx подменяет #окно целиком, и элемент, на котором стоял фокус,
// исчезает: фокус падал на <body>, и человек с клавиатуры начинал путь
// заново с первой ссылки. Правило одно: нажали вкладку — фокус на
// выбранной вкладке новой полосы; нажали раздел — на выбранном разделе;
// экран сменился иначе (ссылка, хэш) — на заголовке, чтобы скринридер
// назвал, куда попали; экран тот же (действие) — остаёмся, где были.
let focusWas = null;
let focusPending = false; // на этот запрос фокус ещё не поставлен
const focusOrigin = (node) => {
  if (!node || node === document.body) return null;
  if (node.closest('.tabs-bar')) return { look: 'tab' };
  if (node.closest('.subtabs-bar')) return { look: 'subtab' };
  // поле поиска стоит в колонке разделов настроек, но разделом не
  // является: после подмены фокус возвращается в само поле, а не на
  // выбранный раздел, иначе набор обрывался бы на первой же букве
  if (node.id === 'search') return { look: 'other', elId: 'search' };
  if (node.closest('#panel')) return { look: 'section' };
  if (node.closest('#menu')) return { look: 'menu' };
  return { look: 'other', elId: node.id || '' };
};
const restoreFocus = (controllerChanged, evt) => {
  // подмен за один запрос две — #окно и меню oob, порядок не обещан.
  // Решаем один раз, на подмене самого окна: там уже есть и новая полоса
  // вкладок, и новый заголовок. На подмене меню решать рано — вкладок
  // ещё нет, и фокус уходил на заголовок старого экрана, который вот-вот
  // исчезнет вместе с фокусом
  const target = evt && evt.target;
  if (!focusPending || !target || !(target.id === 'window' || (target.querySelector && target.querySelector('#screen')))) return;
  focusPending = false;
  const was = focusWas;
  focusWas = null;
  const focusOn = (sel) => { const el = document.querySelector(sel); if (!el) return false; el.focus({ preventScroll: true }); return true; };
  if (was && was.look === 'tab' && focusOn('#window .tabs-bar .segment[aria-selected="true"]')) return;
  if (was && was.look === 'subtab' && focusOn('#window .subtabs-bar .segment[aria-selected="true"]')) return;
  if (was && was.look === 'section' && focusOn('#panel .item.selected')) return;
  if (was && was.look === 'menu' && focusOn('#menu .item.selected')) return;
  if (!controllerChanged && was && was.elId && focusOn('#' + CSS.escape(was.elId))) return;
  // экран сменился, а прежнего места на нём нет — заголовок
  if (controllerChanged || (was && was.look !== 'other')) focusOn('#window h1');
};
document.body.addEventListener('htmx:after:settle', () => { applyContainers(); applyGroups(); });
document.body.addEventListener('htmx:after:swap', (evt) => {
  const fragmentTarget = evt.target && evt.target.classList && evt.target.classList.contains('fragment-body');
  if (fragmentTarget) {
    // приехала ленивая часть: вернуть раскрытые строки и прокрутку,
    // какой она была на момент запроса экрана
    expandedIds.forEach((elId) => { const det = document.getElementById(elId); if (det && !det.open) det.open = true; });
    const feed = document.getElementById('feed');
    if (feed && topAtRequest.screen && topAtRequest.screen === screenNow()) {
      feed.scrollTop = topAtRequest.top;
      feedWas = { screen: topAtRequest.screen, top: topAtRequest.top };
    }
    markScrolls();
    return;
  }
  if (screenNow() !== screenWas) expandedIds.clear();
  expandedIds.forEach((elId) => { const det = document.getElementById(elId); if (det && !det.open) det.open = true; });
  restoreFocus(screenNow() !== screenWas, evt);
  // раскрытая ленивая часть после подмены экрана снова пуста — просим
  // её заново с тем же пределом и поиском, что были у человека
  document.querySelectorAll('#window details.fragment[open]').forEach((det) => loadFragment(det));
  // на телефоне меню лежит поверх экрана: выбрал раздел — оно уходит.
  // Раньше клиент открывал его заново после каждой перерисовки, и
  // человек видел рывок: меню исчезло и тут же вернулось поверх того,
  // что он открыл
  const phone = window.matchMedia('(max-width: 720px)').matches;
  const screenChanged = screenNow() !== screenWas;
  if (phone && screenChanged) {
    ['menu', 'panel'].forEach((nm) => sema.panel(nm, false));
  }
  if (!(phone && screenChanged)) {
    sheetsWere.forEach((nm) => sema.panel(nm, true));
  }
  const feed = document.getElementById('feed');
  if (feed && feedWas.screen && feedWas.screen === screenNow() && !document.getElementById('input')) feed.scrollTop = feedWas.top;
  applyContainers();
  applyGroups();
  markScrolls();
});

// ---- «дальше есть» у рядов, которые прокручиваются вбок ----
// Вкладки и чипы на телефоне не переносятся на второй ряд, а едут вбок:
// иначе шапка вырастает выше самого списка. Полосы прокрутки на телефоне
// не видно, и «История · 497» просто кончалась за краем экрана — ряд
// выглядел обрезанным, а не прокручиваемым. Край растворяем маской: она
// накладывается на рамку элемента, а не на содержимое, поэтому едет
// вместе с рядом и говорит «дальше есть» ровно пока дальше есть.
const sideStrips = () => document.querySelectorAll('.chips, .segments, .filters-bar .segments, .tabs-bar .segments, .subtabs-bar .segments');
const markRow = (node) => {
  const slack = 2;
  const left = node.scrollLeft > slack;
  const right = node.scrollLeft + node.clientWidth < node.scrollWidth - slack;
  node.classList.toggle('scrolls-left', left);
  node.classList.toggle('scrolls-right', right);
};
// Выбранную вкладку подводим к середине её ряда.
//
// На телефоне полоса вкладок едет вбок, и «Спам» — последняя из шести —
// целиком оставалась за правым краем: человек открывал её нажатием в
// шторке, экран менялся, а в полосе выбранного пункта не было видно
// вовсе. Выглядело так, будто ничего не выбрано.
//
// Ставим в середину, а не «лишь бы попало в кадр»: у середины видно
// обоих соседей, и понятно, что ряд едет в обе стороны. Прыжок без
// плавности — ряд перерисовывается вместе с экраном, и анимация здесь
// читалась бы как самопроизвольное движение.
const centerChosen = (strip) => {
	const v = strip.querySelector('.segment.selected, .chip.selected');
	if (!v || strip.scrollWidth <= strip.clientWidth) return;
	// положение вкладки считается от самого ряда, а не offsetLeft: тот
	// меряется от ближайшего позиционированного предка, и это не ряд
	const leftPos = v.getBoundingClientRect().left - strip.getBoundingClientRect().left + strip.scrollLeft;
	const want = leftPos - (strip.clientWidth - v.offsetWidth) / 2;
	const limit = strip.scrollWidth - strip.clientWidth;
	// левый край выбранной не режется никогда: у последней вкладки правое
	// поле ряда (растворение края) шире остатка, и «Безопасность» на
	// 320 точках уезжала на две точки за левый край
	strip.scrollLeft = Math.min(Math.max(0, Math.min(want, limit)), Math.max(0, leftPos - 4));
};
// Сначала все чтения, потом все записи. Чередование «прочитал ширину —
// прокрутил — прочитал следующий ряд» заставляет браузер пересчитывать
// раскладку на каждом ряду: замер 12 сентября 2026 — 53–61 мс раскладки
// при CPU ×4 только на подводке вкладок
const markScrolls = () => {
  const slack = 2;
  const measures = Array.from(sideStrips()).map((strip) => {
    const v = strip.querySelector('.segment.selected, .chip.selected');
    // положение выбранной — от самого ряда: offsetLeft меряется от
    // ближайшего позиционированного предка (это body), и последняя
    // вкладка уезжала за левый край ряда на две точки
    return { strip, width: strip.scrollWidth, win: strip.clientWidth, leftPos: strip.scrollLeft,
      chosen: v ? { leftPos: v.getBoundingClientRect().left - strip.getBoundingClientRect().left + strip.scrollLeft, width: v.offsetWidth } : null };
  });
  measures.forEach((m) => {
    let leftPos = m.leftPos;
    if (m.chosen && m.width > m.win) {
      const want = m.chosen.leftPos - (m.win - m.chosen.width) / 2;
      // левый край выбранной не режется никогда: правое поле ряда шире
      // остатка, и в самом конце прокрутки первая точка вкладки уходила
      leftPos = Math.min(Math.max(0, Math.min(want, m.width - m.win)), Math.max(0, m.chosen.leftPos - 4));
      if (leftPos !== m.leftPos) m.strip.scrollLeft = leftPos;
    }
    m.strip.classList.toggle('scrolls-left', leftPos > slack);
    m.strip.classList.toggle('scrolls-right', leftPos + m.win < m.width - slack);
  });
};
document.body.addEventListener('scroll', (ev) => {
  const node = ev.target;
  if (node && node.classList && (node.classList.contains('chips') || node.classList.contains('segments'))) markRow(node);
}, true);
window.addEventListener('resize', markScrolls);
document.addEventListener('DOMContentLoaded', markScrolls);
// ---- выбор писем для группового действия ----
// Полоса появляется только когда что-то отмечено: пустая панель над
// списком отнимала бы место у самих писем. Номера отмеченных уходят на
// сервер полем «выбрано» (hx-include у кнопки с целью «$selected»).
const selectBar = () => {
  const section = document.querySelector('.section-card:has(.row-check)');
  if (!section) return;
  const checked = document.querySelectorAll('.row-check input:checked').length;
  let bar = document.getElementById('select-bar');
  if (!bar) {
    bar = document.createElement('div');
    bar.id = 'select-bar';
    bar.className = 'select-bar';
    bar.innerHTML = '<span class="count"></span>' +
      '<button type="button" class="action" data-select="all">Выбрать все</button>' +
      '<button type="button" class="action" data-select="none">Снять</button>';
    section.parentNode.insertBefore(bar, section);
  }
  bar.querySelector('.count').textContent = 'Выбрано ' + checked;
  bar.classList.toggle('shown', checked > 0);
  // Групповые действия («Не спам», «В спам выбранные») сервер кладёт в
  // заголовок раздела, и там они звали к себе всегда — даже когда не
  // отмечено ничего и нажать их нельзя. Заголовок из-за них соперничал
  // со строками. Теперь они переезжают в полосу выбора: появляются
  // вместе с ней и исчезают вместе с ней.
  const head = section.parentNode.querySelector('.section-header .right-side');
  if (head) {
    head.querySelectorAll('[data-bulk]').forEach((ctl) => bar.appendChild(ctl));
    if (!head.children.length) head.remove();
  }
};
document.body.addEventListener('change', (ev) => {
  if (ev.target.matches('.row-check input')) selectBar();
});
document.body.addEventListener('click', (ev) => {
  const ctl = ev.target.closest('[data-select]');
  if (!ctl) return;
  const all = ctl.dataset['select'] === 'all';
  document.querySelectorAll('.row-check input').forEach((frm) => { frm.checked = all; });
  selectBar();
});
document.body.addEventListener('htmx:after:swap', selectBar);

document.body.addEventListener('htmx:after:swap', () => {
  // экран сменился — кладём его адрес в хэш, не трогая историю лишний раз
  const currentScreen = screenNow();
  if (currentScreen && currentScreen !== screenFromHash()) history.pushState(null, '', '#screen=' + encodeURIComponent(currentScreen));
  // экран без переписки сбрасывает память о последнем разговоре: иначе
  // возврат в тот же разговор не считался бы новым показом
  if (document.getElementById('input')) sema.bottom(); else sema._screen = '';
  const field = document.getElementById('input');
  if (field) sema.field(field);
});

// текущий экран уходит с каждым запросом заголовком: htmx 4 не шлёт ничего
// про экран сам, а без него сервер после действия не знает, куда вернуть,
// и падал на «Почту»
// связи с сервером нет — говорим об этом, а не оставляем «Подключаюсь…»
// навсегда. Статус 0 — запрос не дошёл вовсе
document.body.addEventListener('htmx:response:error', (evt) => {
  const resp = evt.detail && evt.detail.ctx && evt.detail.ctx.response;
  if (resp && resp.status === 0) sema.banner('Нет связи с Сёмой. Проверьте сеть — страница попробует снова сама.');
});
// в htmx 4 сетевая ошибка — исключение fetch, событие htmx:error;
// после него htmx:after:request не приходит, занятость снимаем здесь
document.body.addEventListener('htmx:error', (evt) => {
  const ctx = evt.detail && evt.detail.ctx;
  const node = ctx && ctx.sourceElement;
  if (node && node.classList) node.classList.remove('busy');
  if (ctx && ctx.response) return;
  // Связи нет, и снимка этого экрана служебный работник тоже не нашёл.
  // Говорим оба возможных «почему» — не хранится вовсе или ещё не
  // открывали: гадать за человека хуже, чем назвать ему обе причины
  // (offline-snapshot, «Экран, который хранить нельзя»)
  if (document.getElementById('screen')) {
    sema.banner('Связи нет, а снимка этого экрана на устройстве нет: он либо не сохраняется никогда (секреты, «Что ушло наружу», «Вход и доступ», закрытые ящики), либо вы его ещё не открывали при связи.');
    return;
  }
  sema.banner('Нет связи с Сёмой. Проверьте сеть — страница попробует снова сама.');
});

// сессии нет — сервер отвечает 401. htmx 4 подменил бы окно телом отказа,
// поэтому подмену отменяем (событие отменяемо) и уходим на страницу входа,
// чтобы вернуться на этот же экран
document.body.addEventListener('htmx:response:error', (evt) => {
  const resp = evt.detail && evt.detail.ctx && evt.detail.ctx.response;
  if (!resp || resp.status !== 401) return;
  evt.preventDefault();
  // вход кончился — снимок, черновики и очередь уходят вместе с сессией: до
  // чужого браузера, у которого сессию отозвали, дотянуться больше нечем
  // (offline-snapshot 4.4)
  goToLogin();
});

// Версия сборки оболочки: подставляется сервером при отдаче файла. Ответ
// сервера несёт свою в заголовке X-Sema-Build; разошлись — оболочка
// устарела (служебный работник отдал вчерашнюю), и страница перезагружается
// один раз. Второй раз с тем же расхождением не перезагружаемся — иначе
// бесконечный круг, — а честно говорим «обновите клиент».
const BUILD = '__СБОРКА__';
const checkBuild = (resp) => {
  const atServer = resp && resp.headers && resp.headers.get && resp.headers.get('X-Sema-Build');
  if (!atServer || atServer === BUILD) return;
  let was = '';
  try { was = sessionStorage.getItem('сёма-перезагрузка') || ''; } catch (e) {}
  if (was !== atServer) {
    try { sessionStorage.setItem('сёма-перезагрузка', atServer); } catch (e) {}
    const refresh = navigator.serviceWorker && navigator.serviceWorker.getRegistration
      ? navigator.serviceWorker.getRegistration().then((res) => (res ? res.update().then(() => res) : null)).catch(() => null)
      : Promise.resolve(null);
    refresh.then((res) => {
      // оболочку работник отдаёт из кэша своей сборки: перезагрузка, пока
      // новый работник ещё ставится, снова открыла бы прежнюю. Он возьмёт
      // страницу сам — controllerchange ниже перезагрузит её уже с новой
      // оболочкой; срок — на случай, если установка не дойдёт до конца
      if (res && (res.installing || res.waiting)) {
        setTimeout(() => location.reload(), 15000);
        return;
      }
      location.reload();
    });
    return;
  }
  sema.banner('Оболочка устарела: у вас сборка ' + BUILD + ', у сервера ' + atServer + '. Закройте все вкладки Сёмы и откройте заново.');
};
document.body.addEventListener('htmx:after:request', (evt) => {
  const ctx = evt.detail && evt.detail.ctx;
  if (ctx && ctx.response) checkBuild(ctx.response);
});

// ---- Снимок экрана (offline-snapshot, группа 4) ----
//
// Складывает снимок страница, достаёт — служебный работник (sw.js): он один
// видит сетевую неудачу, в которой снимок и нужен. Почему пишет страница, а
// не он, подробно сказано в sw.js: прочитать тело ответа в работнике нельзя
// ни клоном (чтение странице встаёт насовсем), ни пересборкой ответа (у
// собранного `url` пустой, и htmx стирает хвост `#screen=…` из адреса).
// Странице читать нечего: разметку она только что нарисовала, и все
// признаки сервера стоят в ней атрибутами.
//
// Признаки читаются только через getAttribute: кириллические data-атрибуты
// в dataset не попадают вовсе — записанные грабли проекта.
const SNAPSHOT_CACHE = 'сёма-снимки'; // то же имя, что в sw.js
const wipeSnapshots = () => {
  try { return caches.delete(SNAPSHOT_CACHE).catch(() => {}); } catch (e) { return Promise.resolve(); }
};
// Уход на вход — одна дверь, и она уносит с устройства всё, что принесла
// сессия: снимок экранов, черновики и очередь исходящих. Это письма и файлы
// человека, лежащие на диске браузера; сессию отозвали — дотянуться до
// чужой машины больше нечем, поэтому стирать надо здесь и сразу
// (offline-snapshot 4.4, сценарий «Чужая машина»).
//
// Функция звалась из очереди исходящих и не существовала вовсе: на 401
// прогон очереди падал ReferenceError, человек оставался без страницы входа,
// а замок отпускался с исключением — и тот же прогон шёл второй раз уже без
// замка. Одно необъявленное имя ломало три вещи сразу.
// leavingForLogin — страница уже уходит на вход: провод ей больше не нужен.
// Подмена окна после отказа всё равно доходит до htmx:after:swap, и провод
// открывался бы ровно в тот миг, когда страницу уводят, — лишний стук, за
// которым и сервер, и журнал доступа видели бы ещё одну попытку.
let leavingForLogin = false;
const goToLogin = () => {
  leavingForLogin = true;
  const go = () => { location.href = '/login?next=' + encodeURIComponent(location.pathname + location.search); };
  Promise.all([wipeSnapshots(), wipeOutbox()]).then(go, go);
};
// снимокСнят — время получения того, что сейчас на экране; null — экран живой
let snapshotAt = null;
const snapshotTime = (iso) => {
  try {
    const det = new Date(iso);
    const time = det.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    const today = new Date().toDateString() === det.toDateString();
    return today ? time : det.toLocaleDateString([], { day: 'numeric', month: 'long' }) + ', ' + time;
  } catch (e) { return iso; }
};
// Действия, которые нельзя отложить, в снимке не показываются: нажать их
// без связи нельзя, а кнопка, которая ничего не делает, хуже её
// отсутствия. Откладываемое называет сервер, клиент только слушается.
const hideNonDeferrable = (root) => {
  root.querySelectorAll('[hx-post]').forEach((node) => {
    if (node.hasAttribute('data-deferrable') || node.closest('[data-deferrable]')) return;
    const target = node.closest('label.select') || node;
    target.classList.add('not-in-snapshot');
  });
};
const showSnapshot = () => {
  const sect = document.getElementById('screen');
  if (!sect) return;
  let head = document.getElementById('snapshot-header');
  if (!snapshotAt) {
    if (head) head.remove();
    sect.classList.remove('snapshot');
    sect.querySelectorAll('.not-in-snapshot').forEach((node) => node.classList.remove('not-in-snapshot'));
    return;
  }
  if (!head) {
    head = document.createElement('div');
    head.id = 'snapshot-header';
    head.className = 'snapshot-header';
    head.setAttribute('role', 'status');
  }
  head.textContent = 'Снимок от ' + snapshotTime(snapshotAt) + ' — связи нет. Свежее Сёма покажет, как только она появится.';
  if (sect.firstChild !== head) sect.insertBefore(head, sect.firstChild);
  sect.classList.add('snapshot');
  hideNonDeferrable(sect);
};
// Живой ответ сервера несёт X-Sema-Build, снимок — X-Sema-Snapshot-At и
// нарочно не несёт сборку: иначе страница ушла бы в перезагрузку по кругу,
// сверяя нынешнюю оболочку со вчерашним заголовком. По этой же паре и
// различаем, откуда приехала разметка. Имена заголовков латиницей: у
// Headers.get кириллическое имя — не «нет такого», а исключение на каждом
// обмене с htmx (поймано в Chrome 27 сентября 2026).
const noteSnapshot = (resp) => {
  const headers = resp && resp.headers && resp.headers.get ? resp.headers : null;
  if (!headers) return;
  const takenAt = headers.get('X-Sema-Snapshot-At');
  if (takenAt) { snapshotAt = takenAt; return; }
  if (headers.get('X-Sema-Build')) snapshotAt = null;
};
['htmx:before:swap', 'htmx:after:request'].forEach((name) => document.body.addEventListener(name, (evt) => {
  const ctx = evt.detail && evt.detail.ctx;
  if (ctx && ctx.response) { noteSnapshot(ctx.response); showSnapshot(); }
}));
document.body.addEventListener('htmx:after:swap', () => showSnapshot());

// ---- Складывание снимка (4.1, 4.2 и стирание из 4.4) ----
//
// Ключи — те же, что читает работник: адрес экрана и версия словаря. Ни
// поиска, ни предела, ни фильтра: это один и тот же экран, а не разные.
const snapshotKey = (dict, screen) =>
  new Request(location.origin + '/снимок?словарь=' + encodeURIComponent(dict) + '&экран=' + encodeURIComponent(screen));
const SNAPSHOT_DICT_KEY = '/снимок-словарь';
const LAST_SNAPSHOT_KEY = '/снимок-последний';

// разметкаДляСнимка — то, что прислал сервер: она целиком лежит в #окно, и
// брать её оттуда честнее, чем перезапрашивать. Клиентские наросты убираем:
// шапка снимка, панель очереди и указатель потягивания рисуются заново, а в
// снимке были бы неправдой — снимком снимка. Застывшее «Обновляю…» на
// вчерашнем экране было бы худшей из них.
const markupForSnapshot = () => {
  const win = document.getElementById('window');
  if (!win) return '';
  const copy = win.cloneNode(true);
  copy.querySelectorAll('#snapshot-header, #outbox, #pull-refresh').forEach((node) => node.remove());
  copy.querySelectorAll('.not-in-snapshot').forEach((node) => node.classList.remove('not-in-snapshot'));
  return copy.innerHTML;
};

const putSnapshot = async (text, screen, dict, days) => {
  if (!text || !screen || !dict || !(days > 0)) return;
  const cache = await caches.open(SNAPSHOT_CACHE);
  const was = await cache.match(SNAPSHOT_DICT_KEY);
  if ((was ? await was.text() : '') !== dict) {
    // версия словаря сменилась — прежние снимки стираются, а не
    // показываются устаревшими: разметка прежнего словаря нарисовала бы
    // неправду (offline-snapshot 4.4)
    for (const ctl of await cache.keys()) await cache.delete(ctl);
    await cache.put(SNAPSHOT_DICT_KEY, new Response(dict));
  }
  // Имена заголовков — только латиницей: HTTP терпит лишь ISO-8859-1, и
  // кириллическое имя роняет сам конструктор Headers. Здесь на этом
  // обожглись: снимок молча не складывался вовсе (27 сентября 2026).
  // X-Sema-Build в снимок не кладём нарочно — иначе страница сверяла бы
  // нынешнюю оболочку со вчерашней сборкой и ушла в перезагрузку по кругу
  await cache.put(snapshotKey(dict, screen), new Response(text, {
    headers: {
      'Content-Type': 'text/html; charset=utf-8',
      'X-Sema-Snapshot-At': new Date().toISOString(),
      'X-Sema-Snapshot-Days': String(days),
    },
  }));
  await cache.put(LAST_SNAPSHOT_KEY, new Response(screen));
};

const dropScreenSnapshot = async (dict, screen) => {
  if (!dict || !screen) return;
  const cache = await caches.open(SNAPSHOT_CACHE);
  await cache.delete(snapshotKey(dict, screen));
};

(() => {
  let when = {};   // экран → когда сложили: экран перечитывается раз в секунды, а снимок так часто не нужен
  let wiped = false;
  document.body.addEventListener('htmx:after:swap', () => {
    const sect = document.getElementById('screen');
    if (!sect || snapshotAt) return;                 // снимок снимка не складываем
    const dict = sect.getAttribute('data-dictionary') || '';
    const screen = sect.getAttribute('data-screen') || '';
    const days = parseInt(sect.getAttribute('data-snapshot-days') || '', 10);
    if (!dict || !screen) return;                    // разметка прежней сборки — молчание значит запрет
    // отметки «моё устройство» нет (сняли её или её и не было) — снимка не
    // остаётся вовсе, а не только у этого экрана
    if (sect.getAttribute('data-own-device') !== '1') {
      if (!wiped) { wiped = true; wipeSnapshots(); }
      return;
    }
    wiped = false;
    if (sect.getAttribute('data-storable') !== '1' || !(days > 0)) {
      // прежний снимок этого экрана мог лечь, когда правила были другими
      dropScreenSnapshot(dict, screen).catch(() => {});
      return;
    }
    // поиск — не экран, а ответ на вопрос: сложить его под именем экрана
    // значило бы подменить снимок выборкой из трёх строк
    const search = document.getElementById('search');
    if (search && search.value) return;
    if (Date.now() - (when[screen] || 0) < 10000) return;
    when[screen] = Date.now();
    if (sect.getAttribute('data-snapshot-differs') !== '1') {
      putSnapshot(markupForSnapshot(), screen, dict, days).catch(() => {});
      return;
    }
    // Снимочная сборка отличается от показанной — на устройство должна лечь
    // не она, а второй, честно отсеянный ответ (решение 8а): в показанной
    // есть письма закрытых ящиков, а им на диске места нет
    const headers = phone() ? { 'X-Sema-Phone': '1' } : {};
    fetch('/ui?screen=' + encodeURIComponent(screen) + '&snapshot=1', { credentials: 'same-origin', headers: headers })
      .then((o) => (o.ok ? o.text() : null))
      .then((touch) => (touch ? putSnapshot(touch, screen, dict, days) : null))
      .catch(() => {});
  });
})();

if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
  // нажали на пуш, а окно уже открыто: работник не перезагружает его (это
  // стёрло бы набранное), а просит открыть нужный экран
  navigator.serviceWorker.addEventListener('message', (evt) => {
    const msg = evt.data;
    if (msg && msg.sema === 'open' && typeof msg.screen === 'string' && msg.screen) openScreen(msg.screen);
  });
  // подготовка подписки и лечение забытой — после первой отрисовки, без спешки
  window.addEventListener('load', () => setTimeout(() => {
    if (window.isSecureContext) sema.pushPrepare().catch(() => {});
    sema.pushHeal();
  }, 2500));
  // новый работник взял страницу — перезагружаемся один раз, чтобы
  // оболочка была из его кэша, а не из прежнего
  // при самой первой регистрации работник тоже «берёт» страницу — это не
  // смена сборки, перезагружать нечего
  let controllerChanged = !navigator.serviceWorker.controller;
  navigator.serviceWorker.addEventListener('controllerchange', () => {
    if (controllerChanged) return;
    controllerChanged = true;
    location.reload();
  });
}

// Живое обновление: Сёма сообщает по проводу имена изменившихся экранов,
// а перечитываем мы их обычным запросом /ui — теми же атрибутами, что и
// опрос. По проводу не ходит ни разметка, ни данные.
//
// Опрос при этом не исчезает, а становится редким: Safari на телефоне
// рвёт сокеты в фоне, а молчание провода неотличимо от «ничего не
// изменилось». Провод жив — опрос раз в минуту как страховка; оборвался —
// обратно к прежней частоте. Молчание — не сигнал.
(() => {
  if (!('WebSocket' in window)) return;
  let sock = null, pause = 1000, reopenTimer = null, aliveSince = 0, closingSelf = false, frames = 0, opens = 0;
  // диагностика из консоли: сёма.провод() — состояние, кадры, открытия
  let lastClose = null; // почему закрылся: для диагностики из консоли
  sema.wire = () => ({ open: !!sock && sock.readyState === WebSocket.OPEN, frames, opens, pause, lastClose,
    poll: (document.getElementById('screen') || {}).getAttribute && document.getElementById('screen').getAttribute('hx-trigger') });
  const url = () => (location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/api/ws';
  const screenEl = () => document.getElementById('screen');

  // частота опроса приходит с сервера в разметке, не зашита здесь
  const setPoll = (wired) => {
    const el = screenEl();
    if (!el || !el.hasAttribute('hx-trigger') || !el.getAttribute('hx-trigger').includes('every')) return;
    // data-атрибуты с кириллицей в dataset не переводятся в camelCase:
    // ключ так и остаётся «опрос-живой», поэтому читаем атрибут напрямую
    const s = el.getAttribute(wired ? 'data-poll-wired' : 'data-poll');
    if (!s) return;
    el.setAttribute('hx-trigger', 'sema:refresh, every ' + s + 's');
    htmx.process(el, true); // без true htmx 4 не переинициализирует узел и интервал остаётся старым
  };
  // после каждой подмены экран новый: частоту ставим заново
  document.body.addEventListener('htmx:after:swap', () => setPoll(!!sock && sock.readyState === WebSocket.OPEN));

  const matchesScreen = (signal, openAddr) => signal === openAddr || openAddr.startsWith(signal + '@');
  // человек правит поле в ленте — перечитывание ждёт, пока он не уйдёт из
  // него: иначе форма порогов на «Здоровье» перерисуется под руками
  let postponed = false;
  const editing = () => {
    const a = document.activeElement;
    if (!a || a.id === 'input' || a.id === 'search') return false;
    return /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName) && !!a.closest('#feed');
  };
  // палец на ленте или лента ещё катится по инерции — перечитывание ждёт.
  // Подмена разметки посреди прокрутки гасит инерцию на iOS: список
  // замирает под пальцем, и это читается как рывок
  let scrolledAt = 0;
  let fingerOnFeed = false;
  document.body.addEventListener('scroll', (ev) => { if (ev.target && ev.target.id === 'feed') scrolledAt = Date.now(); }, true);
  document.body.addEventListener('touchstart', (ev) => { fingerOnFeed = !!(ev.target && ev.target.closest && ev.target.closest('#feed')); }, { passive: true });
  document.body.addEventListener('touchend', () => { fingerOnFeed = false; }, { passive: true });
  document.body.addEventListener('touchcancel', () => { fingerOnFeed = false; }, { passive: true });
  const coasting = () => fingerOnFeed || Date.now() - scrolledAt < 500;
  const refresh = () => {
    const el = screenEl();
    if (!el) return;
    // несохранённое сильнее свежести: перечитывание подменяет ленту
    // целиком, и правка, которую человек ещё не сохранил, исчезла бы под
    // руками — а провод срабатывает как раз тогда, когда что-то менялось
    if (editing() || coasting() || (sema.unsaved && sema.unsaved())) { postponed = true; return; }
    postponed = false;
    htmx.trigger(el, 'sema:refresh');
  };
  document.body.addEventListener('focusout', () => { if (postponed) setTimeout(refresh, 50); });
  // отложенное из-за прокрутки ждёт покоя: раз в полсекунды смотрим, не
  // остановился ли список. Ждать события «прокрутка кончилась» нельзя —
  // в Safari его нет
  setInterval(() => { if (postponed && !editing() && !coasting()) refresh(); }, 500);
  const frame = (text) => {
    let screens = [], serverBuild = '';
    try {
      const res = JSON.parse(text);
      screens = res.экраны || [];
      serverBuild = res.сборка || '';
    } catch (e) { return; }
    // что-то изменилось — подогретое могло устареть
    if (screens.length && sema.prefetchDrop) sema.prefetchDrop();
    // сборка сервера едет в каждом кадре: вкладка, открытая до выката,
    // живёт на проводе и htmx-запросов может не делать вовсе — тогда
    // расхождение замечать было нечем
    if (serverBuild) checkBuild({ headers: { get: (name) => (name === 'X-Sema-Build' ? serverBuild : null) } });
    const openAddr = screenNow();
    if (screens.some((s) => matchesScreen(s, openAddr))) { refresh(); return; }
    // экран не наш — но счётчики в меню могли измениться; меню приезжает
    // вместе с экраном (hx-select-oob), отдельный запрос только когда
    // экран не перечитывается
    if (screens.some((s) => /^(notifications|health|inbox|chats)/.test(s))) {
      const menu = document.getElementById('menu');
      if (menu) htmx.trigger(menu, 'sema:menu');
    }
  };

  let asking = false;
  const askWhoWeAre = () => {
    if (asking) return;
    asking = true;
    fetch('/api/auth/state', { credentials: 'same-origin', cache: 'no-store' })
      .then((resp) => (resp.ok ? resp.json() : null))
      .then((st) => {
        asking = false;
        if (st && st.restricted) { clearTimeout(reopenTimer); goToLogin(); }
      })
      .catch(() => { asking = false; }); // нет связи — это обрыв, провод переоткроется сам
  };

  const openWire = () => {
    if (sock || leavingForLogin || document.visibilityState === 'hidden') return;
    let s;
    try { s = new WebSocket(url()); } catch (e) { return; }
    sock = s;
    let opened = false;
    s.onopen = () => { opened = true; aliveSince = Date.now(); opens++; setPoll(true); };
    s.onmessage = (ev) => { frames++; frame(ev.data); };
    s.onclose = (ev) => {
      lastClose = { code: ev.code, reason: ev.reason, bySelf: closingSelf, visibility: document.visibilityState, when: new Date().toISOString() };
      // Рукопожатие, которое сервер отклонил, браузер показывает только
      // кодом 1006: отказ по входу не отличить от обрыва сети. Если провод не
      // открылся ни разу — спрашиваем открытую ручку, кто мы такие; сессия
      // по фразе снаружи экранов не отдаёт, и стучаться проводом каждые
      // пару секунд ей бессмысленно: уводим на вход, как это делает отказ
      // экрана (1 октября 2026: четыре попытки за десять секунд)
      if (!opened && !closingSelf) askWhoWeAre();
      // закрыться мог старый сокет уже после того, как открыли новый:
      // обнуляем ссылку только на себя
      if (sock !== s && sock !== null) return;
      sock = null;
      setPoll(false);
      if (closingSelf) { closingSelf = false; return; }
      // пауза растёт до минуты и сбрасывается после живого соединения —
      // иначе после ночи первый же выкат стоил бы минуты молчания
      if (aliveSince && Date.now() - aliveSince > 60000) pause = 1000;
      aliveSince = 0;
      clearTimeout(reopenTimer);
      reopenTimer = setTimeout(openWire, pause);
      pause = Math.min(pause * 2, 60000);
    };
    s.onerror = () => { try { s.close(); } catch (e) {} };
  };
  const closeWire = (who) => {
    if (!sock) return;
    lastClose = { who, visibility: document.visibilityState, when: new Date().toISOString() };
    closingSelf = true;
    try { sock.close(); } catch (e) {}
    sock = null;
  };

  // возвращение из фона: за время сна сигналы потеряны — переподключаемся
  // и перечитываем экран один раз; уход — закрываем сами, не дожидаясь
  // системы
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') {
      const el = screenEl();
      if (el) htmx.trigger(el, 'sema:refresh');
      clearTimeout(reopenTimer); pause = 1000; openWire();
    } else {
      closeWire('скрыли');
    }
  });
  window.addEventListener('pagehide', () => closeWire('pagehide'));
  window.addEventListener('online', () => { clearTimeout(reopenTimer); pause = 1000; openWire(); });

  // первый экран уже запрошен выше; провод — после него
  document.body.addEventListener('htmx:after:swap', openWire, { once: true });
})();

// ── Письмо: «как в письме» или «текстом» ──────────────────────────────
// Разметка письма живёт в своём документе (`/письмо/<id>`) и в песочнице:
// у неё нет доступа ни к cookie Сёмы, ни к этой странице. Отсюда две
// мелочи, которые иначе делались бы сами собой: переключатель вида и
// Рамки писем: адрес приезжает в data-src, ставим его после того, как
// рамка оказалась в документе. Рамка, вставленная подменой htmx вместе с
// src, загрузку не начинала вовсе — человек видел пустое белое поле там,
// где письмо (14 сентября 2026), и письмо появлялось только если
// переустановить адрес.
const wakeMailFrames = () => {
  document.querySelectorAll('iframe.mail-frame[data-src]').forEach((iframeEl) => {
    const url = iframeEl.getAttribute('data-src');
    if (!url || iframeEl.getAttribute('src') === url) return;
    iframeEl.setAttribute('src', url);
  });
};
document.body.addEventListener('htmx:after:settle', wakeMailFrames);
document.body.addEventListener('htmx:after:swap', wakeMailFrames);
document.addEventListener('DOMContentLoaded', wakeMailFrames);

// высота рамки — измерить чужой документ снаружи нельзя, и он сообщает
// её сам сообщением наверх.
document.body.addEventListener('click', (evt) => {
  const button = evt.target.closest('.view-switch .view');
  if (!button || button.classList.contains('grow') || button.classList.contains('translation')) return;
  evt.preventDefault();
  const wrap = button.closest('.mail-view');
  if (!wrap) return;
  const iframeEl = wrap.querySelector('.mail-frame');
  if (button.classList.contains('translation')) {
    // перевод — то же письмо теми же местами: меняем только адрес рамки
    const url = wrap.getAttribute('data-url') || '';
    const was = iframeEl.getAttribute('src') || '';
    const toTranslation = !was.includes('перевод=1');
    let next = url;
    if (toTranslation) next += (url.includes('?') ? '&' : '?') + 'перевод=1';
    if (was.includes('картинки=1')) next += (next.includes('?') ? '&' : '?') + 'картинки=1';
    iframeEl.src = next;
    button.classList.toggle('selected', toTranslation);
    button.textContent = toTranslation ? 'Подлинник' : 'Перевод ' + (button.dataset.translation || '');
    return;
  }
  if (button.classList.contains('images')) {
    // человек решил сам: только теперь браузер идёт за картинками, и
    // только теперь отправитель узнаёт, что письмо открыли
    const url = wrap.getAttribute('data-url') || '';
    iframeEl.src = url + (url.includes('?') ? '&' : '?') + 'картинки=1';
    button.remove();
    const caption = wrap.querySelector('.caption');
    if (caption) caption.remove();
    return;
  }
  wrap.setAttribute('data-view', button.dataset.view || 'as-mail');
  wrap.querySelectorAll('.view-switch .view').forEach((btn) => {
    if (!btn.classList.contains('images')) btn.classList.toggle('selected', btn === button);
  });
  const text = wrap.querySelector('.mail-as-text');
  if (text) text.hidden = button.dataset.view === 'as-mail';
});

// «Развернуть» — рамка на всю высоту окна. Высоту письма снаружи не
// измерить: документ в непрозрачном источнике, а пускать в него скрипты
// ради одной высоты значило бы держать стену на одном ключе. Рамку можно
// и просто потянуть за угол — это умеет сам браузер.
document.body.addEventListener('click', (evt) => {
  const button = evt.target.closest('.view-switch .view.grow');
  if (!button) return;
  evt.preventDefault();
  const wrap = button.closest('.mail-view');
  if (!wrap) return;
  const grown = wrap.classList.toggle('full-height');
  button.textContent = grown ? 'Свернуть' : 'Развернуть';
  wrap.querySelector('.mail-frame').style.height = '';
});

// ---- Поисковый выбор из общего набора ----
// У «Моделей» девять ролей, у каждой список основной и запасной модели,
// а моделей подключено 54. В разметке это 972 одинаковые строчки
// вариантов — больше трети всех узлов экрана. Теперь набор приезжает
// один раз, а список собирается в тот миг, когда его открыли, и один
// и тот же на все строки.
//
// Сам список остаётся <select> с единственным выбранным вариантом: так
// у него сохраняются имя поля, hx-vals и отправка по change — менять
// проездную часть не пришлось, поменялось только, откуда берутся
// варианты.
const optionSets = () => {
  const nodeEl = document.getElementById('option-sets');
  if (!nodeEl) return {};
  try { return JSON.parse(nodeEl.textContent) || {}; } catch (e) { return {}; }
};

let pickerFor = null;
const closePicker = () => {
  const win = document.getElementById('set-picker');
  if (win) win.remove();
  pickerFor = null;
};

const openPicker = (picker) => {
  const set = optionSets()[picker.dataset['optionSet']] || [];
  if (!set.length) return false;
  closePicker();
  pickerFor = picker;
  const win = document.createElement('div');
  win.id = 'set-picker';
  win.className = 'set-picker';
  win.innerHTML =
    '<div class="search-box"><input class="search" type="search" placeholder="Найти" autocomplete="off"></div>' +
    '<div class="options" role="listbox"></div>';
  document.body.appendChild(win);

  const field = win.querySelector('input');
  const listBox = win.querySelector('.options');
  const currentValue = picker.value;
  const draw = (what) => {
    listBox.innerHTML = '';
    what.slice(0, 200).forEach((v) => {
      const row = document.createElement('button');
      row.type = 'button';
      row.className = 'option' + (v === currentValue ? ' selected' : '');
      row.setAttribute('role', 'option');
      row.setAttribute('aria-selected', v === currentValue ? 'true' : 'false');
      row.textContent = v;
      row.addEventListener('click', () => choose(picker, v));
      listBox.appendChild(row);
    });
    if (!what.length) {
      const none = document.createElement('div');
      none.className = 'nothing';
      none.textContent = 'ничего не нашлось';
      listBox.appendChild(none);
    }
  };
  draw(set);
  field.addEventListener('input', () => {
    const words = field.value.trim().toLowerCase();
    draw(words ? set.filter((v) => v.toLowerCase().includes(words)) : set);
  });
  field.addEventListener('keydown', (ev) => {
    if (ev.key === 'Escape') { closePicker(); picker.focus(); return; }
    if (ev.key === 'Enter') {
      const first = listBox.querySelector('.option');
      if (first) { ev.preventDefault(); choose(picker, first.textContent); }
      return;
    }
    if (ev.key === 'ArrowDown') {
      const first = listBox.querySelector('.option');
      if (first) { ev.preventDefault(); first.focus(); }
    }
  });
  listBox.addEventListener('keydown', (ev) => {
    const buttons = [...listBox.querySelectorAll('.option')];
    const i = buttons.indexOf(document.activeElement);
    if (ev.key === 'ArrowDown' && i >= 0 && i < buttons.length - 1) { ev.preventDefault(); buttons[i + 1].focus(); }
    if (ev.key === 'ArrowUp') { ev.preventDefault(); (i > 0 ? buttons[i - 1] : field).focus(); }
    if (ev.key === 'Escape') { closePicker(); picker.focus(); }
  });

  // над строкой, если снизу не помещается: список из пятидесяти моделей
  // у нижней роли иначе уезжает за край экрана
  const box = picker.getBoundingClientRect();
  const height = Math.min(360, window.innerHeight - 40);
  win.style.width = Math.max(box.width, 260) + 'px';
  win.style.left = Math.min(box.left, window.innerWidth - win.offsetWidth - 12) + 'px';
  win.style.top = (box.bottom + height > window.innerHeight ? Math.max(8, box.top - height - 6) : box.bottom + 4) + 'px';
  field.focus();
  return true;
};

// выбрать — кладём значение в сам <select> и отдаём его htmx тем же
// событием, каким он отдавал выбор из выпадашки
const choose = (picker, value) => {
  if (![...picker.options].some((o) => o.value === value)) {
    picker.add(new Option(value, value));
  }
  picker.value = value;
  closePicker();
  picker.dispatchEvent(new Event('change', { bubbles: true }));
};

document.body.addEventListener('mousedown', (ev) => {
  const picker = ev.target.closest && ev.target.closest('select[data-option-set]');
  if (picker) {
    // своя выпадашка вместо родной: родная показала бы один вариант
    if (openPicker(picker)) ev.preventDefault();
    return;
  }
  if (pickerFor && !ev.target.closest('#set-picker')) closePicker();
});
document.body.addEventListener('keydown', (ev) => {
  const picker = ev.target.closest && ev.target.closest('select[data-option-set]');
  if (picker && (ev.key === 'Enter' || ev.key === ' ' || ev.key === 'ArrowDown')) {
    if (openPicker(picker)) ev.preventDefault();
  }
});
document.body.addEventListener('htmx:before:request', closePicker);

// ---- нижняя полоса сохранения настроек ----
//
// Настройки не сохраняются на лету. Правка копится в самой форме, а
// внизу экрана стоит полоса: сколько правок не сохранено, «Сохранить» и
// «Отменить». До этого кнопка «Сохранить» была у каждой формы, и на
// «Почте» их пять: человек менял три настройки, нажимал одну кнопку и
// уходил — две правки пропадали молча.
//
// Что изменено, спрашиваем у самого браузера: у поля есть значение
// сейчас и значение из разметки (defaultValue, defaultChecked,
// defaultSelected). Своего учёта нет — значит ему нечему разойтись с
// тем, что человек видит, а «Отменить» это обычный form.reset().
(() => {
  const forms = () => Array.from(document.querySelectorAll('form.deferred'));
  const controlsOf = (box) => Array.from(box.querySelectorAll('input, select, textarea'))
    .filter((el) => el.type !== 'hidden');
  const touched = (el) => {
    if (el.type === 'checkbox' || el.type === 'radio') return el.checked !== el.defaultChecked;
    if (el.tagName === 'SELECT') return Array.from(el.options).some((v) => v.selected !== v.defaultSelected);
    return el.value !== el.defaultValue;
  };
  // считаем полями, а не элементами: сегментный переключатель это
  // несколько радиокнопок, и смена выбора трогает сразу две — «Изменено
  // 2» вместо одного было бы неправдой
  const formChanges = (frm) => Array.from(frm.querySelectorAll('.field')).filter((p) => controlsOf(p).some(touched));
  const allChanged = () => forms().flatMap((frm) => formChanges(frm).map((p) => ({ form: frm, field: p })));
  const labelOf = (p) => ((p.querySelector('label') || {}).textContent || '').trim();
  // что было и что стало — словами человека, а не «true → false»
  const inWords = (p) => {
    // несколько вариантов чипами («Источники»): было и стало — списком имён, а
    // не «включено» по первому флажку
    const chips = Array.from(p.querySelectorAll('.field-chips input[type=checkbox]'));
    if (chips.length) {
      const names = (on) => chips.filter(on).map((c) => (c.parentElement.textContent || '').trim()).join(', ') || 'ничего';
      return [names((c) => c.defaultChecked), names((c) => c.checked)];
    }
    const box2 = p.querySelector('input[type=checkbox]');
    if (box2) return [box2.defaultChecked ? 'включено' : 'выключено', box2.checked ? 'включено' : 'выключено'];
    const radios = Array.from(p.querySelectorAll('input[type=radio]'));
    if (radios.length) {
      const was = radios.find((res) => res.defaultChecked), now = radios.find((res) => res.checked);
      return [was ? was.value : 'пусто', now ? now.value : 'пусто'];
    }
    // окно «с … до …»: два поля времени — одна правка, и называется она
    // окном целиком, а не половиной
    const times = Array.from(p.querySelectorAll('input[type=time]'));
    if (times.length === 2) {
      const span = (from, to) => (from || to ? (from || '…') + '–' + (to || '…') : 'круглые сутки');
      return [span(times[0].defaultValue, times[1].defaultValue), span(times[0].value, times[1].value)];
    }
    const picker = p.querySelector('select');
    if (picker) {
      const was = Array.from(picker.options).find((v) => v.defaultSelected);
      return [was ? was.value : 'пусто', picker.value || 'пусто'];
    }
    const field = p.querySelector('input, textarea');
    if (!field) return ['', ''];
    // пароль и секрет в полосу не выписываем: набранное закрыто так же, как в
    // самом поле, а полоса видна и тому, кто стоит за плечом
    if (field.type === 'password' || field.hasAttribute('data-secure')) return ['закрыто', field.value ? 'новое значение' : 'без изменений'];
    return [field.defaultValue || 'пусто', field.value || 'пусто'];
  };

  const bar = () => document.getElementById('save-bar');
  const leavePanel = () => document.getElementById('leave');
  const hasChanges = () => allChanged().length > 0;
  // провод и опрос перечитывают экран, когда что-то изменилось на
  // сервере, — и стёрли бы несохранённое прямо под руками. Пока правка
  // не сохранена и не отменена, экран остаётся тем, что видит человек
  sema.unsaved = hasChanges;
  // Поле поиска в шапке перечитывает экран, и вместе с ним исчезло бы
  // несохранённое: человек выбрал ответы у трёх инструментов, вспомнил про
  // поиск и потерял выбор молча. Пока правка не сохранена и не отменена,
  // поиск на сервер не ходит, а полоса мигает — так видно, чего она ждёт
  // (переход по ссылке при несохранённом уже спрашивает панель «уйдёте —
  // пропадёт»; поиск — не переход, и у него своей панели нет)
  document.body.addEventListener('htmx:before:request', (evt) => {
    const src = evt.target;
    if (!src || src.id !== 'search' || !hasChanges()) return;
    evt.preventDefault();
    const p = bar();
    if (!p) return;
    p.classList.remove('attention');
    void p.offsetWidth; // та же анимация заново
    p.classList.add('attention');
  });

  const changeLine = (p) => {
    const [was, now] = inWords(p);
    return '«' + labelOf(p) + '» — ' + was + ' → ' + now;
  };
  const text = (node, chunks) => {
    node.textContent = '';
    chunks.forEach((ctl) => {
      if (typeof ctl === 'string') { node.append(ctl); return; }
      const nodeEl = document.createElement(ctl.tag);
      nodeEl.textContent = ctl.text;
      if (ctl.cls) nodeEl.className = ctl.cls;
      node.append(nodeEl);
    });
  };

  const refresh = () => {
    const p = bar();
    if (!p) return;
    const changes = allChanged();
    const troubles = document.querySelectorAll('form.deferred .not-saved:not([hidden])').length;
    // помечаем тронутое поле точкой: иначе на длинном экране человек не
    // найдёт то, о чём говорит счётчик внизу
    forms().forEach((frm) => frm.querySelectorAll('.field').forEach((field) => field.classList.remove('changed')));
    changes.forEach((p2) => p2.field.classList.add('changed'));
    p.hidden = changes.length === 0 && troubles === 0;
    if (p.hidden) { hideLeave(); return; }
    const chunks = ['Изменено ', { tag: 'b', text: String(changes.length) }];
    if (changes.length === 1) chunks.push(': ' + changeLine(changes[0].field));
    else if (changes.length > 1) chunks.push(': ' + changeLine(changes[0].field) + ' и ещё ' + (changes.length - 1));
    if (troubles) chunks.push(' · ', { tag: 'span', cls: 'trouble', text: troubles + ' не сохранилось' });
    text(p.querySelector('.what'), chunks);
  };

  // ---- сохранение ----
  // Формы сохраняются по одной и ответом получают «да или нет», а не
  // экран целиком: подмена экрана после первой же формы стёрла бы
  // несохранённое в остальных.
  const acceptValues = (frm) => controlsOf(frm).forEach((el) => {
    if (el.type === 'checkbox' || el.type === 'radio') el.defaultChecked = el.checked;
    else if (el.tagName === 'SELECT') Array.from(el.options).forEach((v) => { v.defaultSelected = v.selected; });
    else el.defaultValue = el.value;
  });
  const trouble = (frm, errText) => {
    const place = frm.querySelector('.not-saved');
    if (!place) return;
    place.textContent = '';
    if (!errText) { place.hidden = true; return; }
    const dotEl = document.createElement('span'); dotEl.className = 'dot';
    const body = document.createElement('div'); body.className = 'text';
    const head = document.createElement('b'); head.textContent = 'Не сохранилось';
    body.append(head, document.createTextNode(errText));
    const again = document.createElement('button');
    again.type = 'button'; again.className = 'action'; again.textContent = 'Повторить';
    again.addEventListener('click', () => save([frm]));
    place.append(dotEl, body, again);
    place.hidden = false;
  };
  const postForm = async (frm) => {
    const data = new FormData(frm);
    data.set('screen', screenNow());
    let resp;
    try {
      // адрес экрана — в заголовке закодированным (сервер раскодирует,
      // currentScreen): кириллица в значении заголовка роняет fetch, и
      // «Сохранить» отвечало «Сёма не ответил» на экране ящика «проба» или
      // сервера «хозяин». htmx-запросы кодируют его так же (выше)
      resp = await fetch('/do', {
        method: 'POST', body: data, credentials: 'same-origin',
        headers: { 'HX-Request': 'true', 'HX-Current-Screen': encodeURIComponent(screenNow()), 'X-Sema-Save': '1' },
      });
    } catch (e) { return 'Сёма не ответил: проверьте связь и нажмите «Повторить».'; }
    if (resp.status === 401) {
      // снимок, черновики и очередь уходят вместе с сессией (offline-snapshot 4.4)
      goToLogin();
      return 'Сессия кончилась — войдите заново.';
    }
    let outcome = {};
    try { outcome = await resp.json(); } catch (e) {}
    if (outcome['ок']) { acceptValues(frm); return ''; }
    return outcome['ошибка'] || ('Сёма ответил ' + resp.status + ', а почему — не сказал.');
  };
  let saving = false;
  const save = async (which) => {
    if (saving) return;
    const picker = (which || forms()).filter((frm) => formChanges(frm).length > 0);
    if (!picker.length) return;
    saving = true;
    setBusy(true);
    let allSaved = true;
    for (const frm of picker) {
      const err = await postForm(frm);
      trouble(frm, err);
      if (err) allSaved = false;
    }
    setBusy(false);
    saving = false;
    refresh();
    // перечитываем экран только когда сохранилось всё: иначе подмена
    // унесла бы и правку, которая не прошла, и её объяснение
    if (allSaved) openScreen(screenNow(), 0, bar(), true);
  };
  const setBusy = (yes) => {
    const p = bar();
    if (p) p.querySelectorAll('.action').forEach((ctl) => ctl.classList.toggle('busy', yes));
  };

  const revert = () => {
    forms().forEach((frm) => { frm.reset(); trouble(frm, ''); });
    refresh();
  };

  // ---- уход с несохранённым ----
  // Панель местная: она всплывает под вкладками, у того места, куда
  // человек нажал, и не перекрывает экран. Системного окна браузера тут
  // нет — оно не умеет назвать, что именно пропадёт, а «Уверены?» без
  // названия закрывают не глядя.
  let pendingTarget = null; // куда человек собрался, пока не ответил
  const hideLeave = () => {
    const u = leavePanel();
    if (u) u.hidden = true;
    pendingTarget = null;
  };
  const leadsAway = (node) => {
    if (!node) return null;
    // ищем нажатое, а не любого предка с hx-get: сама секция экрана
    // несёт hx-get своего перечитывания, и по предкам сюда попадал любой
    // щелчок на экране — даже переключение тумблера. Тумблер при этом
    // отменялся вместе с событием и молча возвращался обратно
    const target = node.closest('a, button, [data-goto], [data-action]');
    if (!target) return null;
    if (target.closest('#leave, #save-bar, form.deferred')) return null;
    if (target.dataset['goto'] || target.dataset['action'] === '$more') return target;
    return (target.getAttribute('hx-get') || '').startsWith('/ui?screen=') ? target : null;
  };
  document.body.addEventListener('click', (evt) => {
    if (!hasChanges() || !evt.target.closest) return;
    const where = leadsAway(evt.target);
    if (!where || where === pendingTarget) return;
    evt.preventDefault();
    evt.stopImmediatePropagation();
    const u = leavePanel();
    if (!u) return;
    pendingTarget = where;
    // на телефоне разделы приезжают шторкой снизу, и нажатие пришло из
    // неё: панель ухода всплыла бы за шторкой, и человек увидел бы, что
    // ничего не произошло. Шторку закрываем — вопрос важнее списка
    ['menu', 'panel'].forEach((nm) => sema.panel(nm, false));
    const changes = allChanged();
    // у узла графа текст — подсказка и подпись вперемешку: имя берём из aria-label
    const box = (where.getAttribute('aria-label') || where.textContent || '').trim().replace(/\s+/g, ' ');
    text(u.querySelector('.what'), [
      changeLine(changes[0].field) + (changes.length > 1 ? ' и ещё ' + (changes.length - 1) : '') +
      '. Уйдёте' + (box ? ' на «' + box + '»' : '') + ' — изменение пропадёт.',
    ]);
    u.hidden = false;
    u.scrollIntoView({ block: 'nearest' });
    const stayBtn = u.querySelector('[data-leave="stay"]');
    if (stayBtn) stayBtn.focus();
  }, true);
  document.body.addEventListener('click', (evt) => {
    const button = evt.target.closest('[data-leave]');
    if (!button) return;
    const where = pendingTarget;
    if (button.dataset['leave'] === 'stay' || !where) { hideLeave(); return; }
    // человек решил уйти: правки больше не наши, и тот же элемент
    // нажимается ещё раз — со всеми своими hx-атрибутами, какими бы они
    // ни были. Собирать запрос за него значило бы повторять htmx
    forms().forEach((frm) => { frm.reset(); trouble(frm, ''); });
    const p = bar();
    if (p) p.hidden = true;
    hideLeave();
    pendingTarget = where;
    where.click();
    pendingTarget = null;
  });

  document.body.addEventListener('click', (evt) => {
    const button = evt.target.closest('[data-save]');
    if (!button) return;
    if (button.dataset['save'] === 'save') save(null);
    else revert();
  });
  // Enter в поле формы: у отложенной формы нет кнопки отправки, и
  // браузер отправил бы её обычным способом — уводя со страницы
  document.body.addEventListener('submit', (evt) => {
    if (!evt.target.classList || !evt.target.classList.contains('deferred')) return;
    evt.preventDefault();
    save(null);
  });
  document.body.addEventListener('input', (evt) => {
    if (evt.target.closest && evt.target.closest('form.deferred')) refresh();
  });
  document.body.addEventListener('change', (evt) => {
    if (evt.target.closest && evt.target.closest('form.deferred')) refresh();
  });
  document.body.addEventListener('htmx:after:swap', () => { hideLeave(); refresh(); });
})();

// ---- строки настройки ----
//
// Форма со Style «rows» (разметка — markup.go, «field-row»): подсказка
// стоит одной строкой с хвостом, нажатие раскрывает её; «по умолчанию»
// кладёт умолчание в контрол и больше ничего не делает — сохраняет его
// нижняя полоса, как любую другую правку, и она же считает её. Видна
// кнопка, пока значение расходится с умолчанием: сервер ставит `is-default`
// при показе, а здесь класс держится в такт тому, что человек набирает.
(() => {
  const rowOf = (el) => (el && el.closest ? el.closest('.field.setting') : null);
  const controlsOf = (row) => Array.from(row.querySelectorAll('input, select, textarea')).filter((el) => el.type !== 'hidden');
  const isOn = (v) => ['true', 'on', '1', 'да'].includes(String(v).trim().toLowerCase());
  const valueOf = (row) => {
    const list = controlsOf(row);
    const box = list.find((el) => el.type === 'checkbox');
    if (box) return box.checked ? 'true' : 'false';
    const radios = list.filter((el) => el.type === 'radio');
    if (radios.length) { const on = radios.find((el) => el.checked); return on ? on.value : ''; }
    // окно «с … до …»: два поля времени — одно значение «02:00-06:00»;
    // пусто в обоих — «круглые сутки», то есть пустое умолчание
    const times = list.filter((el) => el.type === 'time');
    if (times.length === 2) { const [from, to] = times.map((el) => el.value); return from || to ? from + '-' + to : ''; }
    return list.length ? String(list[0].value).trim() : '';
  };
  const sync = (row) => {
    const def = row.getAttribute('data-default');
    if (def === null) return;
    const now = valueOf(row);
    const same = row.getAttribute('data-kind') === 'toggle' ? isOn(now) === isOn(def) : now === String(def).trim();
    row.classList.toggle('is-default', same);
  };
  const put = (row, value) => {
    const list = controlsOf(row);
    const parts = String(value).split('-');
    let timeAt = 0;
    list.forEach((el) => {
      if (el.type === 'checkbox') el.checked = isOn(value);
      else if (el.type === 'radio') el.checked = el.value === value;
      else if (el.type === 'time') el.value = parts[timeAt++] || '';
      else el.value = value;
    });
    // те же события, что даёт человек руками: полоса сохранения слушает их
    const first = list.find((el) => el.type !== 'radio' || el.checked) || list[0];
    if (first) {
      first.dispatchEvent(new Event('input', { bubbles: true }));
      first.dispatchEvent(new Event('change', { bubbles: true }));
    }
    sync(row);
  };
  document.body.addEventListener('click', (evt) => {
    const reset = evt.target.closest && evt.target.closest('.to-default');
    if (reset) {
      const row = rowOf(reset);
      if (row) put(row, row.getAttribute('data-default'));
      return;
    }
    const hint = evt.target.closest && evt.target.closest('.setting-hint');
    if (hint) {
      const open = hint.classList.toggle('open');
      hint.setAttribute('aria-expanded', open ? 'true' : 'false');
    }
  });
  document.body.addEventListener('keydown', (evt) => {
    if (evt.key !== 'Enter' && evt.key !== ' ') return;
    const hint = evt.target.closest && evt.target.closest('.setting-hint');
    if (!hint) return;
    evt.preventDefault();
    hint.click();
  });
  const onEdit = (evt) => { const row = rowOf(evt.target); if (row) sync(row); };
  document.body.addEventListener('input', onEdit);
  document.body.addEventListener('change', onEdit);
})();

// ---- ленивые части: содержимое раздела приезжает при раскрытии ----
// Свёрнутый раздел с сорока чатами и семью тумблерами у каждого весил две
// тысячи узлов, которых человек не видел. Теперь раздел приезжает
// заголовком, а тело просит у сервера часть — по её адресу, с пределом и
// поиском, которые помним здесь: после подмены экрана раскрытая часть
// возвращается такой, какой её оставили.
const fragmentStates = new Map(); // адрес части → { предел, поиск }
const loadFragment = (det, anew) => {
  const fragment = det.getAttribute('data-fragment');
  const body = det.querySelector('.fragment-body');
  if (!fragment || !body) return;
  if (!anew && body.getAttribute('data-loaded') === '1') return;
  const p = fragmentStates.get(fragment) || {};
  let url = '/ui?screen=' + encodeURIComponent(screenNow()) + '&part=' + encodeURIComponent(fragment);
  if (p.limit) url += '&limit=' + p.limit;
  // поле поиска на экране одно: если у части своего нет, в неё уходит
  // набранное в поле экрана
  const screenSearch = ((document.getElementById('search') || {}).value || '').trim();
  const search = p.search || screenSearch;
  if (search) url += '&search=' + encodeURIComponent(search);
  body.setAttribute('data-loaded', '1');
  const field = det.querySelector('.fragment-search input');
  if (field && field.value !== (p.search || '')) field.value = p.search || '';
  // Без source и с целью-селектором. Проверено в Chrome 12 сентября 2026:
  // с source htmx наследует у его предков атрибуты запроса, а секция
  // #экран несёт hx-select-oob перечитывания — и тело части оставалось
  // пустым молча; элемент вместо селектора в target он тоже не принимает
  const target = '#' + body.id;
  const p2 = htmx.ajax('GET', url, { target: target, swap: 'innerHTML' });
  // след для отладки в браузерных тестах: что просили и чем кончилось
  (window.__fragments = (window.__fragments || []).slice(-19)).push({ url, target, exists: !!document.querySelector(target), outcome: 'ждём' });
  const record = window.__fragments[window.__fragments.length - 1];
  Promise.resolve(p2).then((res) => { record.outcome = 'ок ' + String(res && res.status); }, (ev) => { record.outcome = 'ошибка ' + ev; });
  return p2;
};
// toggle не всплывает — ловим на стадии захвата
document.body.addEventListener('toggle', (evt) => {
  const det = evt.target;
  if (det && det.matches && det.matches('details.fragment') && det.open) loadFragment(det);
}, true);
// ---- свёрнутые контейнеры панели ----
// Свернуть группу с топиками или канал — жест человека, и он должен
// держаться: пока раскрытие решал сервер по адресу экрана, контейнер с
// открытым топиком не сворачивался вовсе (запись экрана 13 сентября
// 2026). Помним свёрнутые, а не раскрытые: свежий контейнер приезжает
// раскрытым, и человеку не нужно открывать каждый заново
const collapsedContainers = new Set(JSON.parse(localStorage.getItem('сёма.свёрнутые') || '[]'));
const applyContainers = () => {
  document.querySelectorAll('#panel [data-container]').forEach((row) => {
    const url = row.getAttribute('data-container');
    const collapsed = collapsedContainers.has(url);
    const button = row.querySelector('.collapse-container');
    if (button) {
      button.classList.toggle('expanded', !collapsed);
      button.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
    }
  });
  document.querySelectorAll('#panel [data-inside]').forEach((row) => {
    // класс, а не hidden: тем же флагом пользовались группы, и второй
    // код перетирал первому — строки вылезали обратно
    row.classList.toggle('in-collapsed-container', collapsedContainers.has(row.getAttribute('data-inside')));
  });
};
// Свёрнутые группы списка чатов. Прячем строки атрибутом и правилом
// стилей, а не флагом hidden: флаг ставили два независимых кода (группы и
// контейнеры), и второй перетирал первому — чаты вылезали обратно
// (записи экрана 13 сентября 2026). Атрибут у строки один, правило одно,
// перетирать нечего
const collapsedGroups = new Set(JSON.parse(localStorage.getItem('сёма.группы') || '[]'));
const applyGroups = () => {
  document.querySelectorAll('#panel button.group').forEach((head) => {
    const name = head.getAttribute('data-group');
    const collapsedGroup = collapsedGroups.has(name);
    head.classList.toggle('expanded', !collapsedGroup);
    head.setAttribute('aria-expanded', collapsedGroup ? 'false' : 'true');
    const rows = document.querySelectorAll(`#panel .panel-row[data-in-group="${name}"]`);
    rows.forEach((el) => { el.classList.toggle('in-collapsed-group', collapsedGroup); });
    // свёрнутая группа обязана сказать, сколько в ней спрятано
    let countEl = head.querySelector('.count');
    if (collapsedGroup) {
      if (!countEl) { countEl = document.createElement('span'); countEl.className = 'count'; head.append(countEl); }
      countEl.textContent = String(rows.length);
    } else if (countEl) countEl.remove();
  });
};
sema.panelGroup = (button) => {
  const name = button.getAttribute('data-group');
  if (collapsedGroups.has(name)) collapsedGroups.delete(name);
  else collapsedGroups.add(name);
  try { localStorage.setItem('сёма.группы', JSON.stringify([...collapsedGroups])); } catch (ev) { /* приватное окно */ }
  applyGroups();
};

sema.container = (url) => {
  if (collapsedContainers.has(url)) collapsedContainers.delete(url);
  else collapsedContainers.add(url);
  try { localStorage.setItem('сёма.свёрнутые', JSON.stringify([...collapsedContainers])); } catch (ev) { /* приватное окно */ }
  applyContainers();
};

// Меню «···» закрывается кликом мимо, а не вторым нажатием на сами точки:
// человек уводит палец к строке, меню остаётся висеть поверх неё, и
// следующий клик тратится на закрытие. Так же ведут себя все выпадающие
// меню, к которым люди привыкли. Escape — оттуда же
const closeRowMenus = (except) => {
  document.querySelectorAll('details.row-menu[open]').forEach((det) => { if (det !== except) det.open = false; });
};
document.addEventListener('pointerdown', (evt) => {
  const target = evt.target;
  const ownMenu = target && target.closest ? target.closest('details.row-menu') : null;
  // Нажатие по самим точкам не трогаем вовсе: браузер переключит details
  // сам по клику, а закрытие «всех, кроме своего» на pointerdown гасило
  // только что открытое меню — «3 точки не раскрывает» (13 сентября 2026)
  if (ownMenu) {
    closeRowMenus(ownMenu);
    return;
  }
  closeRowMenus(null);
}, true);
document.addEventListener('keydown', (evt) => {
  if (evt.key === 'Escape') closeRowMenus(null);
});
// выбрали пункт — меню закрывается само, не дожидаясь перерисовки
document.body.addEventListener('click', (evt) => {
  const item = evt.target && evt.target.closest && evt.target.closest('.dropdown .menu-item');
  if (item) closeRowMenus(null);
});
sema.moreInFragment = (det) => {
  const fragment = det.getAttribute('data-fragment');
  const p = fragmentStates.get(fragment) || {};
  fragmentStates.set(fragment, { ...p, limit: (p.limit || 20) * 2 });
  loadFragment(det, true);
};
(() => {
  let timer = null;
  document.body.addEventListener('input', (evt) => {
    const field = evt.target;
    if (!field || !field.closest || !field.closest('.fragment-search')) return;
    const det = field.closest('details.fragment');
    if (!det) return;
    clearTimeout(timer);
    timer = setTimeout(() => {
      const fragment = det.getAttribute('data-fragment');
      // новый поиск — с начала: прежний предел относился к прежнему списку
      fragmentStates.set(fragment, { search: field.value.trim() });
      loadFragment(det, true);
    }, 350);
  });
})();

// ---- ожидание: полоска, если ответа нет дольше 200 мс ----
// Быстрый ответ полоски не заслуживает — мигание хуже ожидания. А после
// 200 мс человек уже не уверен, что нажал: «Здоровье» отвечает за
// четверть секунды, и без знака он нажимал второй раз.
//
// Без счётчика запросов: первая редакция считала «ушёл — пришёл», а
// запрос, который htmx оборвал сам (опрос перебит нажатием), не приходит
// ни ответом, ни ошибкой — и полоска бежала вечно (13 сентября 2026).
// Полоска живёт от последнего запроса и гаснет по любому исходу, а если
// исхода нет вовсе — сама через десять секунд.
(() => {
  let showTimer = null;
  let hideTimer = null;
  const clearBar = () => {
    clearTimeout(showTimer); clearTimeout(hideTimer);
    showTimer = hideTimer = null;
    document.body.classList.remove('waiting');
  };
  document.body.addEventListener('htmx:before:request', () => {
    clearTimeout(showTimer); clearTimeout(hideTimer);
    showTimer = setTimeout(() => document.body.classList.add('waiting'), 200);
    hideTimer = setTimeout(clearBar, 10000);
  });
  ['htmx:after:request', 'htmx:error', 'htmx:abort', 'htmx:after:swap'].forEach((nm) => document.body.addEventListener(nm, clearBar));
})();

// Ленивая подгрузка переписки. Кнопка «Показать более ранние» стоит вверху
// ленты; человек долистал до неё — ранние грузятся сами, нажимать не надо.
// Следим за прокруткой самой ленты, а не окна: прокручивается она. Пока
// идёт подгрузка, второй раз не зовём, а память «человек у низа» держим
// свежей — иначе перечитывание экрана утаскивало бы читающего историю вниз.
document.addEventListener('scroll', (evt) => {
  const feed = evt.target;
  if (!feed || feed.id !== 'feed') return;
  if (sema._loading || feed.scrollTop > 150) return;
  // в окно уже идёт переход — человек уходит, ранние ему не нужны
  if (sema.windowBusy && sema.windowBusy()) return;
  const earlier = feed.querySelector('[data-action="$more"]');
  if (!earlier) return;
  sema._loading = true;
  earlier.click();
}, true);

// место в ленте — до подмены: после неё прокрутка уже сброшена в ноль
document.body.addEventListener('htmx:before:swap', () => sema.rememberPlace());

// Колонка разделов настроек — восемнадцать строк и четыре заголовка, на
// невысоком окне она прокручивается. Переход в раздел подменяет окно
// целиком вместе с колонкой, и прокрутка сбрасывалась в начало: нажал
// «Журнал настроек» внизу — и выбранный раздел уехал за край. Прокрутку
// помним до подмены, а после неё ещё и показываем выбранный пункт.
let columnTop = null;
document.body.addEventListener('htmx:before:swap', () => {
  const column = document.getElementById('panel');
  columnTop = column && column.classList.contains('sections') ? column.scrollTop : null;
});
document.body.addEventListener('htmx:after:swap', () => {
  const column = document.getElementById('panel');
  if (!column || !column.classList.contains('sections')) { columnTop = null; return; }
  if (columnTop !== null) column.scrollTop = columnTop;
  columnTop = null;
  const chosen = column.querySelector('.item.selected');
  if (!chosen) return;
  const was = column.getBoundingClientRect(), now = chosen.getBoundingClientRect();
  if (now.top < was.top || now.bottom > was.bottom) chosen.scrollIntoView({ block: 'nearest' });
});

// Свёрнутые рукой узлы меню. Группы чатов сервер присылает раскрытыми
// всегда — иначе меню прыгало при каждом переходе между группами. Но
// свёрнутое человеком он развернул бы от следующего же нажатия, поэтому
// решение человека помнит клиент и применяет сразу после подмены меню,
// до отрисовки кадра, — без мигания.
const collapsedNodes = new Set((() => {
  try { return JSON.parse(localStorage.getItem('сёма.меню.свёрнуто') || '[]'); } catch (ev) { return []; }
})());
let applyingNodes = false;
const applyMenuNodes = () => {
  applyingNodes = true;
  document.querySelectorAll('#menu details[data-remember]').forEach((u) => {
    if (collapsedNodes.has(u.id) && u.open) u.open = false;
  });
  applyingNodes = false;
};
document.body.addEventListener('toggle', (evt) => {
  const u = evt.target;
  if (applyingNodes || !u || u.tagName !== 'DETAILS' || !u.hasAttribute('data-remember')) return;
  // пока ищут по меню, узлы раскрывает поиск, а не человек
  if (document.getElementById('menu')?.classList.contains('searching')) return;
  if (u.open) collapsedNodes.delete(u.id); else collapsedNodes.add(u.id);
  try { localStorage.setItem('сёма.меню.свёрнуто', JSON.stringify([...collapsedNodes])); } catch (ev) { /* приватное окно */ }
}, true);
document.body.addEventListener('htmx:after:swap', applyMenuNodes);
document.body.addEventListener('htmx:after:settle', applyMenuNodes);
applyMenuNodes();

// Свёрнутые разделы меню («Сёма сам», «Своё»). Подпись раздела — кнопка:
// нажатие прячет пункты этого раздела, решение помнится между переходами
// и применяется сразу после подмены меню, до отрисовки кадра.
const collapsedSections = new Set((() => {
  try { return JSON.parse(localStorage.getItem('сёма.меню.разделы') || '[]'); } catch (ev) { return []; }
})());
const applyMenuSections = () => {
  document.querySelectorAll('#menu button.group[data-menu-section]').forEach((button) => {
    const name = button.getAttribute('data-menu-section');
    const collapsed = collapsedSections.has(name);
    button.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
    document.querySelectorAll('#menu [data-in-section]').forEach((p) => {
      if (p.getAttribute('data-in-section') === name) p.classList.toggle('in-collapsed-section', collapsed);
    });
  });
};
sema.menuSection = (button) => {
  const name = button.getAttribute('data-menu-section');
  if (collapsedSections.has(name)) collapsedSections.delete(name); else collapsedSections.add(name);
  try { localStorage.setItem('сёма.меню.разделы', JSON.stringify([...collapsedSections])); } catch (ev) { /* приватное окно */ }
  applyMenuSections();
};
document.body.addEventListener('htmx:after:swap', applyMenuSections);
document.body.addEventListener('htmx:after:settle', applyMenuSections);
applyMenuSections();

// Счётчик на значке установленного приложения — то, что скажет «есть новое»,
// не открывая Сёму. Пока страница открыта, значок ставит она (ниже); при
// закрытой странице — служебный работник по числу из пуша (sw.js), а
// снимается он здесь же, при первом открытии. Число приходит атрибутом от
// сервера — клиент не разбирает меню и не
// знает, какой там пункт про уведомления.
const updateBadge = () => {
  const menu = document.getElementById('menu');
  if (!menu) return;
  // dataset не переводит кириллические data-атрибуты в camelCase:
  // dataset.ждут — undefined, читать только через getAttribute
  const waiting = parseInt(menu.getAttribute('data-waiting'), 10) || 0;
  try {
    // значок отказывает обещанием, а не броском: пока приложение не
    // установлено, отказ приходит на каждую смену экрана, и без .catch
    // консоль забивается необработанными отказами
    const resp = waiting > 0 ? navigator.setAppBadge?.(waiting) : navigator.clearAppBadge?.();
    if (resp && resp.catch) resp.catch(() => {});
  } catch (e) { /* браузер значка не умеет */ }
};
document.body.addEventListener('htmx:after:swap', updateBadge);
document.body.addEventListener('htmx:after:settle', updateBadge);
updateBadge();

// ---- Подсветка строки после перехода (Document.Highlight) ----
//
// Поиск по настройкам и кнопка замечания «открыть саму настройку» ведут не на
// экран вообще, а к строке: экран открывается, раздел со строкой раскрывается
// (свёрнутый и «Тонкая настройка», которая приезжает по раскрытию), строка
// встаёт в середину окна и вспыхивает. Метку несёт сервер атрибутом
// data-highlight у #screen — только в ответе на переход: перечитывание ленты
// опросом #screen не заменяет, и подсветка не возвращается. Саму строку находим
// по data-hl («f:» — поле формы, «r:» — строка, «s:» — раздел).
//
// Регистрируется последней в файле: прочие обработчики подмены (возврат
// прокрутки, раскрытых строк) сперва делают своё, а мы — после них, иначе
// «верни прокрутку, как была» стирала бы то, к чему мы только что подвели.
let hlPending = null; // { mark, until }
const hlFind = (mark) => document.querySelector('#window [data-hl="' + CSS.escape(mark) + '"]');
const hlTry = () => {
  if (!hlPending) return;
  if (Date.now() > hlPending.until) { hlPending = null; return; }
  const el = hlFind(hlPending.mark);
  if (!el) {
    // строки нет: она может лежать в «Тонкой настройке», тело которой приезжает
    // по раскрытию. Раскрываем и ждём: после подмены тела сюда придём снова
    const rare = document.querySelector('#window details.fragment[data-fragment="rare"]');
    if (rare && !rare.open) rare.open = true;
    return;
  }
  hlPending = null;
  // всё, что прячет строку, — раскрыть: свёрнутый раздел, «Ещё N полей»
  for (let node = el.parentElement; node; node = node.parentElement) {
    if (node.tagName === 'DETAILS' && !node.open) node.open = true;
  }
  // к середине окна, а не к краю: у экрана сущности сверху закреплённая шапка, и
  // строка у верхнего края оказалась бы под ней
  el.scrollIntoView({ block: 'center' });
  el.classList.remove('hl');
  void el.offsetWidth; // перезапустить вспышку, если она уже шла
  el.classList.add('hl');
  setTimeout(() => el.classList.remove('hl'), 2800);
};
document.body.addEventListener('htmx:after:swap', (evt) => {
  const target = evt.target;
  if (target && target.classList && target.classList.contains('fragment-body')) {
    if (hlPending) setTimeout(hlTry, 0);
    return;
  }
  const scr = document.getElementById('screen');
  const want = scr && scr.getAttribute('data-highlight');
  if (!want || !target || !(target.id === 'window' || (target.querySelector && target.querySelector('#screen')))) return;
  // метку берём один раз и снимаем: следующая подмена окна без неё — не переход
  scr.removeAttribute('data-highlight');
  hlPending = { mark: want, until: Date.now() + 8000 };
  hlTry();
});

// «показать» у закрытого поля: в поле пароля macOS держит латинскую
// раскладку, и логин кириллицей не набрать. Нажатие делает поле обычным на
// время набора; перерисовка экрана возвращает закрытое
document.addEventListener('click', (evt) => {
  const btn = evt.target.closest && evt.target.closest('[data-show-secure]');
  if (!btn) return;
  const input = btn.parentElement && btn.parentElement.querySelector('input[data-secure]');
  if (!input) return;
  const show = input.type === 'password';
  input.type = show ? 'text' : 'password';
  btn.textContent = show ? 'скрыть' : 'показать';
  btn.setAttribute('aria-pressed', show ? 'true' : 'false');
  btn.setAttribute('aria-label', show ? 'Скрыть набранное' : 'Показать набранное');
  input.focus();
});

// Масштаб на телефоне запрещён: Сёма — приложение, а не страница. Строку
// viewport с user-scalable=no Safari на iOS ради доступности не слушает, и
// щипок двумя пальцами приближал экран, после чего меню и поле ввода
// уезжали за край. Щипок гасится здесь; двойное касание — CSS
// touch-action, приближение у полей ввода — maximum-scale в viewport
['gesturestart', 'gesturechange', 'gestureend'].forEach((name) => {
  document.addEventListener(name, (evt) => evt.preventDefault(), { passive: false });
});
document.addEventListener('touchmove', (evt) => {
  if (evt.touches && evt.touches.length > 1) evt.preventDefault();
}, { passive: false });

