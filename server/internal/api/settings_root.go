package api

import "strconv"

// buildSettingsGroup — каталог одной группы, без обращения к экранам её разделов.
func (s *Server) buildSettingsGroup(doc *Document, group settingsGroup) {
	doc.Heading = group.Name
	doc.Layout = "wide"
	doc.Components = append(doc.Components, catalogBlocks(group, s.sectionIndex(doc), s.sectionMarks(doc), doc.guest)...)
}

func (s *Server) settingsOverviewCatalog(doc *Document) Component {
	idx, marks := s.sectionIndex(doc), s.sectionMarks(doc)
	catalog := Component{Type: "section", ID: "settings-catalog", Title: "Все настройки"}
	for _, group := range settingsGroups {
		catalog.Children = append(catalog.Children, Component{Type: "section", Title: group.Name,
			Children: catalogBlocks(group, idx, marks, doc.guest)})
	}
	return catalog
}

func catalogBlocks(group settingsGroup, idx map[string]settingsSection, marks map[string]sectionMark, guest bool) []Component {
	var blocks []Component
	for _, block := range group.Blocks {
		part := Component{Type: "section", Title: block.Title}
		for _, addr := range block.Sections {
			if guest && adminSettingsScreen(addr) {
				continue
			}
			row := Component{Type: "row", Style: "catalog-link", Layout: "wide", ID: "catalog-" + addr,
				Subtitle: sectionTitle[addr], Caption: idx[addr].Meta,
				Actions: []Action{{Action: "$goto", Target: addr, Label: sectionTitle[addr]}}}
			if m, ok := marks[addr]; ok {
				text := ""
				if m.Count > 0 {
					text = strconv.Itoa(m.Count)
				}
				row.Badges = []Badge{{Text: text, Level: m.Level}}
			}
			part.Children = append(part.Children, row)
		}
		if len(part.Children) > 0 {
			blocks = append(blocks, part)
		}
	}
	return blocks
}
