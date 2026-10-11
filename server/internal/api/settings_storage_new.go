package api

// «Добавить хранилище» — свой экран `settings:storages@new`, а не две формы под
// списком: девять полей S3 и три поля Диска, развёрнутые всегда, стояли под
// строками хранилищ и были больше самого списка. Вид хранилища — внутренняя
// вкладка (S3-совместимое · Яндекс.Диск): у них разные поля, и показывать нужно
// одну форму.
//
// Что вписано — то и уйдёт в `add_storage` или `add_yadisk`: имена полей прежние.
// Ключи выбираются из «Ключей и секретов» списком — сюда пишется только имя
// секрета, не само значение. После «Добавить» Сёма сам проверяет хранилище и ведёт
// на его экран: итог проверки живёт там.

func (s *Server) buildStorageNew(doc *Document, tab string) error {
	doc.Heading = "Добавить хранилище"
	doc.Subheading = "Зеркало: Сёма кладёт туда копию каждого файла, а читает файлы с диска. Ключи берутся из «Ключей и секретов»"
	doc.Layout = "read"
	doc.Components = append(doc.Components, Component{Type: "crumb",
		Actions: []Action{{Action: "$goto", Target: storagesScreen, Label: "‹ Хранилища"}}})
	base := entityAddr(storagesScreen, storNew, "")
	setSubtabs(doc, base, tab, []innerTab{
		{ID: "", Name: "S3-совместимое"},
		{ID: storNewDisk, Name: "Яндекс.Диск"},
	})
	if tab == storNewDisk {
		// у Диска не bucket и ключи, а папка и токен: мешать их в одну форму значит
		// объяснять в подсказке к каждому полю, кому оно
		doc.Components = append(doc.Components, Component{Type: "form", ID: "storage-new-yadisk", Style: formRows,
			Fields: []Field{
				{Type: "text", Name: "name", Label: "Название", Value: "яндекс-диск", Hint: "как называть его в списке; «new» занято"},
				s.secretField("token_secret", "Токен Диска",
					"выбирается из «Ключей и секретов» — сюда пишется имя секрета, не сам токен. Токен заводится на oauth.yandex.ru: приложение с правами Яндекс.Диска, потом response_type=token"),
				{Type: "text", Name: "folder", Label: "Папка на Диске", Value: "Сёма", Hint: "куда складывать; копии лягут в подпапку «копии»"},
			},
			Submit: &Action{Action: "add_yadisk", Label: "Добавить", Style: "primary"}})
		return nil
	}
	doc.Components = append(doc.Components, Component{Type: "form", ID: "storage-new-s3", Style: formRows,
		Fields: []Field{
			{Type: "text", Name: "name", Label: "Название", Value: "s3", Hint: "например, yandex или minio; «new» занято"},
			{Type: "picker", Name: "preset", Label: "Готовый адрес", Options: presetNames(), Value: "свой адрес",
				Hint: "подставьте адрес известного провайдера или впишите свой ниже"},
			{Type: "text", Name: "endpoint", Label: "Адрес",
				Hint: "storage.yandexcloud.net · s3.eu-central-003.backblazeb2.com · 127.0.0.1:9000 для MinIO на этой машине"},
			{Type: "text", Name: "bucket", Label: "Bucket"},
			s.secretField("access_secret", "Ключ доступа", "выбирается из «Ключей и секретов»"),
			s.secretField("secret_secret", "Секретный ключ", "выбирается из «Ключей и секретов»"),
			{Type: "text", Name: "region", Label: "Регион", Hint: "ru-central1 у Яндекса; для MinIO можно пусто", More: true},
			{Type: "text", Name: "prefix", Label: "Префикс", Value: "sema", Hint: "папка внутри bucket, можно пусто", More: true},
			{Type: "toggle", Name: "ssl", Label: "HTTPS", Value: "true", More: true},
		},
		Submit: &Action{Action: "add_storage", Label: "Добавить", Style: "primary"}})
	return nil
}
