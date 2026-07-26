package access

import "strings"

type Capability struct {
	Name        string
	Label       string
	Description string
	Category    string
}

const (
	View             = "view"
	Status           = "status"
	Start            = "start"
	Stop             = "stop"
	Restart          = "restart"
	Command          = "command"
	ConsoleRead      = "console.read"
	ConsoleWrite     = "console.write"
	FileRead         = "file.read"
	FileWrite        = "file.write"
	ServerJoin       = "server.join"
	WhitelistManage  = "whitelist.manage"
	BackupList       = "backup.list"
	BackupCreate     = "backup.create"
	BackupRestore    = "backup.restore"
	AutomationManage = "automation.manage"
	SecretRead       = "secret.read"
	AccessManage     = "access.manage"
)

var Capabilities = []Capability{
	{Name: View, Label: "View server", Description: "See the server and its connection metadata.", Category: "General"},
	{Name: Status, Label: "View live status", Description: "Read process, player, and resource observations.", Category: "General"},
	{Name: Start, Label: "Start", Description: "Start the game server.", Category: "Lifecycle"},
	{Name: Stop, Label: "Stop", Description: "Stop the game server when operational constraints allow it.", Category: "Lifecycle"},
	{Name: Restart, Label: "Restart", Description: "Restart the game server when operational constraints allow it.", Category: "Lifecycle"},
	{Name: Command, Label: "Approved commands", Description: "Run commands explicitly configured for this server.", Category: "Console"},
	{Name: ConsoleRead, Label: "Read console", Description: "Read persisted and live console output.", Category: "Console"},
	{Name: ConsoleWrite, Label: "Write console", Description: "Send arbitrary game-console input.", Category: "Console"},
	{Name: FileRead, Label: "Read files", Description: "Browse and download files inside managed path roots.", Category: "Files"},
	{Name: FileWrite, Label: "Modify files", Description: "Upload, edit, create, and delete files inside managed path roots.", Category: "Files"},
	{Name: ServerJoin, Label: "Join server", Description: "Join the game server. HOGS automatically enforces this through a game whitelist when supported.", Category: "Players"},
	{Name: WhitelistManage, Label: "Manage whitelist", Description: "Inspect, reconcile, and manage manual whitelist entries.", Category: "Players"},
	{Name: BackupList, Label: "List backups", Description: "View available snapshots.", Category: "Backups"},
	{Name: BackupCreate, Label: "Create backups", Description: "Create a new server snapshot.", Category: "Backups"},
	{Name: BackupRestore, Label: "Restore backups", Description: "Restore a snapshot when deployment policy enables restores.", Category: "Backups"},
	{Name: AutomationManage, Label: "Manage automation", Description: "Create, edit, enable, and delete lifecycle automation rules for this server.", Category: "Automation"},
	{Name: SecretRead, Label: "Reveal server secrets", Description: "Reveal user-facing shared secrets independently of join access.", Category: "Secrets"},
	{Name: AccessManage, Label: "Manage server access", Description: "Create and remove grants for this server only.", Category: "Administration"},
}

var known = func() map[string]bool {
	result := make(map[string]bool, len(Capabilities))
	for _, capability := range Capabilities {
		result[capability.Name] = true
	}
	return result
}()

func Known(name string) bool {
	return known[name]
}

func Grants(granted, requested string) bool {
	return granted == "*" || granted == requested ||
		(granted == Command && strings.HasPrefix(requested, Command+":"))
}

func Names() []string {
	result := make([]string, 0, len(Capabilities))
	for _, capability := range Capabilities {
		result = append(result, capability.Name)
	}
	return result
}
