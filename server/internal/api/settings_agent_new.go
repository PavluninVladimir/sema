package api

// «Пригласить агента» — свой экран `settings:agents@new`, а не форма под
// списком: форма «Позвать агента» стояла развёрнутой всегда, между корневым
// сертификатом и журналом. Имя, роль и кнопка «Создать код»; ниже — приглашения,
// которые ещё ждут своего агента.
//
// Код приглашения показывается **один раз**: сразу после «Создать код» (его
// забирает эта сборка экрана), с сроком действия и кнопкой «Скопировать». Дальше
// приглашение называет не код, а его отпечаток (`Invite.Ref`): «Отменить» у
// строки ждущих носит отпечаток, а сам код не лежит ни в разметке, ни в снимке,
// ни в закладке. Код одноразовый и живёт минуты — потерянный не жалко, выпишется
// новый, а старый отменяется.
//
// Войти агент может только этим кодом: это единственная дверь без сертификата.

import "github.com/PavluninVladimir/sema/server/internal/engine"

func (s *Server) buildAgentNew(doc *Document) error {
	st := s.eng.Store()
	doc.Heading = "Пригласить агента"
	doc.Subheading = "Агент входит один раз — одноразовым кодом, который живёт несколько минут и сгорает при первом же использовании"
	doc.Layout = "read"
	add := func(c Component) { doc.Components = append(doc.Components, c) }
	add(Component{Type: "crumb", Actions: []Action{{Action: "$goto", Target: agentsScreen, Label: "‹ Агенты"}}})

	// только что выданный код: забираем и показываем один раз
	s.mu.Lock()
	fresh := s.freshInvite
	s.freshInvite = nil
	s.mu.Unlock()
	if fresh != nil {
		until := fresh.Until
		add(Component{Type: "section", ID: "invite-fresh", Title: "Код для «" + fresh.Name + "»", Children: []Component{{
			Type: "row", Layout: "wide", ID: "invite-code", Level: LevelWarn, Dot: levelDot(LevelWarn),
			Subtitle: fresh.Code,
			Caption: "роль «" + fresh.Role + "» · действует до " + shortTime(until) + " · одноразовый · показан один раз: " +
				"скопируйте его сейчас и вставьте на машине агента",
			Actions: []Action{{Action: "$copy", Target: fresh.Code, Label: "Скопировать", Style: "primary"}}}}})
	}

	add(Component{Type: "form", ID: "agent-invite", Style: formRows,
		Fields: []Field{
			{Type: "text", Name: "name", Label: "Как назвать", Hint: "мак, рабочая, шлюз; по имени строится адрес экрана, поэтому «new» и «~» нельзя"},
			{Type: "segments", Name: "role", Label: "Роль", Value: engine.RoleResident,
				Options: []string{engine.RoleResident, engine.RoleGateway},
				Hint: "житель машины — карточки кода, память и уведомления у часов; шлюз — вход к Сёме из интернета. " +
					"Роли не совмещаются: житель читает ваши репозитории, шлюз стоит лицом к интернету. " +
					"Роль закрепляется за агентом, и сменить её можно только новым приглашением"},
		},
		Submit: &Action{Action: "invite_agent", Label: "Создать код", Style: "primary"}})

	// живые приглашения: имя, роль, срок — без кода
	if invites, err := st.Invites(); err == nil && len(invites) > 0 {
		var lines []Component
		for _, i := range invites {
			role, _ := engine.KnownRole(i.Role)
			lines = append(lines, Component{Type: "row", Layout: "wide", ID: "invite-" + i.Ref(), Level: LevelInfo, Dot: levelDot(LevelInfo),
				Subtitle: "Для «" + i.Name + "»",
				Caption:  "роль «" + role + "» · действует до " + shortTime(i.Until) + " · одноразовый · код был показан при выдаче",
				// отмена не разрушает ничего ценного: код одноразовый и живёт минуты,
				// новый выпишется за секунду — поэтому кнопка видна, а не под «···»
				Actions: []Action{{Action: "drop_invite", Target: i.Ref(), Label: "Отменить"}}})
		}
		add(Component{Type: "section", ID: "invites-waiting",
			Title: "Ждут агента · " + plural(len(lines), "приглашение", "приглашения", "приглашений"), Children: lines})
	}
	return nil
}
