package engine

// Наборы правил «Linux» и «RouterOS»: готовое заполнение для сервера, у
// которого правил ещё нет. Набор не привязан к серверу — «взять набор»
// копирует образцы, и дальше правила сервера живут своей жизнью: что
// человек убрал, не вернётся, что дописал, останется.
//
// Раньше это были три общих круга на все серверы (чтение, «только человек»,
// «никогда»). Они переехали сюда: живые образцы разложены по наборам в
// хранилище (`store.layOutSharedRules`), а код держит только первичное
// заполнение для пустой базы — списки обзора, которые Сёма когда-то
// засеял, и то, что нельзя выполнять никогда.

import (
	"github.com/PavluninVladimir/sema/server/internal/store"
)

// sshSeedAllow — команды обзора: читают и ничего не меняют. Команды RouterOS
// узнаются по «/» в начале, Linux — остальные.
var sshSeedAllow = []string{
	"uptime", "df", "free", "journalctl", "tail", "head", "cat", "ls", "ps", "top -b -n 1",
	"docker ps", "docker logs", "docker stats --no-stream", "systemctl status", "systemctl list-units",
	"ss", "netstat", "du", "who", "uname", "nproc", "hostname", "date", "id", "env | grep -v", "wc", "grep", "find", "stat", "echo",
	"apt list", "apt-cache policy", "apt-get -s", "dpkg -l", "dpkg-query", "dpkg -s", "last", "lastb", "lastlog", "w",
	"wg show", "fail2ban-client status", "needrestart -b", "ufw status", "iptables -L", "iptables -S", "nft list", "ip",
	"getent", "lsof -i", "lsblk", "mount", "sysctl", "cut", "awk", "sort", "uniq", "sed -n", "tr", "crontab -l",
	"systemctl is-active", "systemctl list-timers", "systemctl list-unit-files", "debsecan", "checkrestart", "timedatectl", "hostnamectl",
	"vmstat", "iostat", "dmesg", "swapon --show", "docker images", "docker inspect", "cat /var/run/reboot-required", "ls /var/run/reboot-required",
	"/* print*", "/system package update check-for-updates", "/system resource print", "/log print", "/export hide-sensitive",
}

// sshSeedAsk — то, что меняет доступ, пользователей, сеть и пакеты: ключи,
// файрвол, маршруты, удаление. Образец встречается в команде где угодно.
var sshSeedAsk = []string{
	"userdel", "adduser", "useradd", "usermod", "passwd", "chpasswd",
	"authorized_keys", "ssh_host_", "sshd_config", "ufw", "iptables -F", "iptables -X",
	"nft flush", "firewall", "ip route", "ip addr", "netplan", "resolv.conf",
	"downgrade", "apt-get remove", "apt remove", "apt purge", "dpkg --purge",
	"drop database", "drop table", "truncate", "mkswap", "fdisk", "parted",
	"/user", "/ip firewall", "/ip address", "/system reset", "/certificate",
}

// sshSeedDeny — необратимое: нужно обоим наборам.
var sshSeedDeny = []string{
	"rm -rf", "rm -r /", "mkfs", "dd", "shutdown", "reboot", "halt", "poweroff", "chmod -R 777",
	"> /dev/sd", "sh -c", "bash -c", "eval", ":(){", "curl | sh", "wget -O- | sh",
	"reset-configuration", "/file remove", "format-drive", "/system upgrade",
}

// ensureRuleSets — первичное заполнение наборов. Только в пустую базу:
// после переезда живых образцов (store.layOutSharedRules) наборы уже
// заполнены тем, что было у человека, и код их не трогает.
func (e *Engine) ensureRuleSets() {
	if !e.store.RuleSetsEmpty() {
		return
	}
	put := func(set, kind string, list []string) {
		for _, p := range list {
			if store.RuleSetOfPattern(p) != set && kind != store.RuleDeny {
				continue
			}
			_ = e.store.AddRuleSetPattern(set, kind, p)
		}
	}
	for _, set := range store.RuleSets {
		put(set, store.RuleAllow, sshSeedAllow)
		put(set, store.RuleAsk, sshSeedAsk)
		put(set, store.RuleDeny, sshSeedDeny)
	}
}

// TakeRuleSet — взять набор серверу: образцы дописываются, а не заменяют
// то, что уже есть. Возвращает, сколько образцов прибавилось.
func (e *Engine) TakeRuleSet(host, set string) (int, error) {
	n, err := e.store.TakeRuleSet(host, set)
	if err != nil {
		return 0, err
	}
	// у сервера, чей род ещё неизвестен, набор и есть подсказка о роде
	if h, ok := e.store.Host(host); ok && h.Kind == "" {
		switch set {
		case store.RuleSetLinux:
			h.Kind = store.HostLinux
		case store.RuleSetRouterOS:
			h.Kind = store.HostRouterOS
		}
		_ = e.store.SaveHost(h)
	}
	return n, nil
}
