package seed

import (
	"database/sql"
	"fmt"
	"log/slog"
)

func SeedGames(db *sql.DB) error {
	games := []struct {
		id                   string
		name                 string
		image                string
		iconPath             string
		gridPath             string
		heroPath             string
		defaultPorts         string
		defaultEnv           string
		minMemoryMB          int
		minCPU               float64
		gsqGameSlug          string
		disabledCapabilities string
	}{
		{
			id:       "minecraft-java",
			name:     "Minecraft: Java Edition",
			image:    "registry.0xkowalski.dev/gamejanitor/minecraft-java",
			iconPath: "/static/games/minecraft/minecraft-icon.ico",
			gridPath: "/static/games/minecraft/minecraft-grid.png",
			heroPath: "/static/games/minecraft/minecraft-hero.png",
			defaultPorts: `[{"name":"game","port":25565,"protocol":"tcp"}]`,
			defaultEnv: `[
				{"key":"EULA","default":"false","label":"Accept Minecraft EULA","type":"boolean"},
				{"key":"GAMEMODE","default":"survival","label":"Game Mode","type":"select","options":["survival","creative","adventure","spectator"]},
				{"key":"MAX_PLAYERS","default":"20","label":"Max Players","type":"number"},
				{"key":"DIFFICULTY","default":"normal","label":"Difficulty","type":"select","options":["peaceful","easy","normal","hard"]},
				{"key":"MOTD","default":"A Gamejanitor Server","label":"Message of the Day"},
				{"key":"PVP","default":"true","label":"PvP","type":"boolean"},
				{"key":"SERVER_PORT","default":"25565","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"5","system":true}
			]`,
			minMemoryMB:          2048,
			minCPU:               1.0,
			gsqGameSlug:          "minecraft",
			disabledCapabilities: `[]`,
		},
		{
			id:       "rust",
			name:     "Rust",
			image:    "registry.0xkowalski.dev/gamejanitor/rust",
			iconPath: "/static/games/rust/rust-icon.ico",
			gridPath: "/static/games/rust/rust-grid.png",
			heroPath: "/static/games/rust/rust-hero.png",
			defaultPorts: `[{"name":"game","port":28015,"protocol":"udp"},{"name":"query","port":28017,"protocol":"udp"},{"name":"rcon","port":28016,"protocol":"tcp"}]`,
			defaultEnv: `[
				{"key":"SERVER_MAXPLAYERS","default":"50","label":"Max Players","type":"number"},
				{"key":"SERVER_HOSTNAME","default":"Gamejanitor Rust Server","label":"Server Name"},
				{"key":"SERVER_WORLDSIZE","default":"3000","label":"World Size","type":"number"},
				{"key":"RCON_PASSWORD","default":"changeme","label":"RCON Password"},
				{"key":"SERVER_PORT","default":"28015","system":true},
				{"key":"QUERY_PORT","default":"28017","system":true},
				{"key":"RCON_PORT","default":"28016","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"15","system":true}
			]`,
			minMemoryMB:          4096,
			minCPU:               2.0,
			gsqGameSlug:          "rust",
			disabledCapabilities: `[]`,
		},
		{
			id:       "ark-survival-evolved",
			name:     "ARK: Survival Evolved",
			image:    "registry.0xkowalski.dev/gamejanitor/ark-survival-evolved",
			iconPath: "/static/games/ark-survival-evolved/ark-survival-evolved-icon.ico",
			gridPath: "/static/games/ark-survival-evolved/ark-survival-evolved-grid.png",
			heroPath: "/static/games/ark-survival-evolved/ark-survival-evolved-hero.png",
			defaultPorts: `[{"name":"game","port":7777,"protocol":"udp"},{"name":"query","port":27015,"protocol":"udp"},{"name":"rcon","port":27020,"protocol":"tcp"}]`,
			defaultEnv: `[
				{"key":"SESSION_NAME","default":"Gamejanitor ARK Server","label":"Session Name"},
				{"key":"MAX_PLAYERS","default":"70","label":"Max Players","type":"number"},
				{"key":"ADMIN_PASSWORD","default":"changeme","label":"Admin Password"},
				{"key":"SERVER_PASSWORD","default":"","label":"Server Password"},
				{"key":"MAP","default":"TheIsland","label":"Map","type":"select","options":["TheIsland","TheCenter","ScorchedEarth_P","Ragnarok","Aberration_P","Extinction","Valguero_P","Genesis","CrystalIsles","LostIsland","Fjordur"]},
				{"key":"GAME_PORT","default":"7777","system":true},
				{"key":"QUERY_PORT","default":"27015","system":true},
				{"key":"RCON_PORT","default":"27020","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"30","system":true}
			]`,
			minMemoryMB:          6144,
			minCPU:               2.0,
			gsqGameSlug:          "arkse",
			disabledCapabilities: `[]`,
		},
		{
			id:       "counter-strike-2",
			name:     "Counter-Strike 2",
			image:    "registry.0xkowalski.dev/gamejanitor/counter-strike-2",
			iconPath: "/static/games/counter-strike-2/counter-strike-2-icon.ico",
			gridPath: "/static/games/counter-strike-2/counter-strike-2-grid.png",
			heroPath: "/static/games/counter-strike-2/counter-strike-2-hero.png",
			defaultPorts: `[{"name":"game","port":27015,"protocol":"udp"},{"name":"rcon","port":27015,"protocol":"tcp"}]`,
			defaultEnv: `[
				{"key":"HOSTNAME","default":"Gamejanitor CS2 Server","label":"Server Name"},
				{"key":"MAXPLAYERS","default":"16","label":"Max Players","type":"number"},
				{"key":"RCON_PASSWORD","default":"changeme","label":"RCON Password"},
				{"key":"GAME_TYPE","default":"0","label":"Game Type","type":"select","options":["0","1","2","3"]},
				{"key":"GAME_MODE","default":"1","label":"Game Mode","type":"select","options":["0","1","2"]},
				{"key":"MAP","default":"de_dust2","label":"Starting Map"},
				{"key":"GAME_PORT","default":"27015","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"5","system":true}
			]`,
			minMemoryMB:          2048,
			minCPU:               2.0,
			gsqGameSlug:          "cs2",
			disabledCapabilities: `[]`,
		},
		{
			id:       "garrysmod",
			name:     "Garry's Mod",
			image:    "registry.0xkowalski.dev/gamejanitor/garrysmod",
			iconPath: "/static/games/garrysmod/garrysmod-icon.ico",
			gridPath: "/static/games/garrysmod/garrysmod-grid.png",
			heroPath: "/static/games/garrysmod/garrysmod-hero.png",
			defaultPorts: `[{"name":"game","port":27015,"protocol":"udp"},{"name":"rcon","port":27015,"protocol":"tcp"}]`,
			defaultEnv: `[
				{"key":"HOSTNAME","default":"Gamejanitor GMod Server","label":"Server Name"},
				{"key":"MAXPLAYERS","default":"16","label":"Max Players","type":"number"},
				{"key":"RCON_PASSWORD","default":"changeme","label":"RCON Password"},
				{"key":"GAMEMODE","default":"sandbox","label":"Game Mode","type":"select","options":["sandbox","terrortown","prop_hunt","murder","deathrun"]},
				{"key":"MAP","default":"gm_flatgrass","label":"Starting Map"},
				{"key":"GAME_PORT","default":"27015","system":true},
				{"key":"RCON_PORT","default":"27015","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"5","system":true}
			]`,
			minMemoryMB:          2048,
			minCPU:               1.0,
			gsqGameSlug:          "garrys-mod",
			disabledCapabilities: `[]`,
		},
		{
			id:       "palworld",
			name:     "Palworld",
			image:    "registry.0xkowalski.dev/gamejanitor/palworld",
			iconPath: "/static/games/palworld/palworld-icon.ico",
			gridPath: "/static/games/palworld/palworld-grid.png",
			heroPath: "/static/games/palworld/palworld-hero.png",
			defaultPorts: `[{"name":"game","port":8211,"protocol":"udp"},{"name":"rcon","port":25575,"protocol":"tcp"}]`,
			defaultEnv: `[
				{"key":"SERVER_NAME","default":"Gamejanitor Palworld Server","label":"Server Name"},
				{"key":"MAX_PLAYERS","default":"32","label":"Max Players","type":"number"},
				{"key":"ADMIN_PASSWORD","default":"changeme","label":"Admin Password"},
				{"key":"SERVER_PASSWORD","default":"","label":"Server Password"},
				{"key":"DIFFICULTY","default":"Normal","label":"Difficulty","type":"select","options":["Casual","Normal","Hard"]},
				{"key":"GAME_PORT","default":"8211","system":true},
				{"key":"RCON_PORT","default":"25575","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"15","system":true}
			]`,
			minMemoryMB:          4096,
			minCPU:               2.0,
			gsqGameSlug:          "",
			disabledCapabilities: `["query"]`,
		},
		{
			id:       "terraria",
			name:     "Terraria",
			image:    "registry.0xkowalski.dev/gamejanitor/terraria",
			iconPath: "/static/games/terraria/terraria-icon.ico",
			gridPath: "/static/games/terraria/terraria-grid.png",
			heroPath: "/static/games/terraria/terraria-hero.png",
			defaultPorts: `[{"name":"game","port":7777,"protocol":"tcp"}]`,
			defaultEnv: `[
				{"key":"WORLD_NAME","default":"Gamejanitor","label":"World Name"},
				{"key":"MAX_PLAYERS","default":"8","label":"Max Players","type":"number"},
				{"key":"PASSWORD","default":"","label":"Server Password"},
				{"key":"DIFFICULTY","default":"1","label":"Difficulty","type":"select","options":["0","1","2","3"]},
				{"key":"WORLD_SIZE","default":"2","label":"World Size","type":"select","options":["1","2","3"]},
				{"key":"GAME_PORT","default":"7777","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"5","system":true}
			]`,
			minMemoryMB:          1024,
			minCPU:               1.0,
			gsqGameSlug:          "terraria",
			disabledCapabilities: `["query"]`,
		},
		{
			id:       "valheim",
			name:     "Valheim",
			image:    "registry.0xkowalski.dev/gamejanitor/valheim",
			iconPath: "/static/games/valheim/valheim-icon.ico",
			gridPath: "/static/games/valheim/valheim-grid.png",
			heroPath: "/static/games/valheim/valheim-hero.png",
			defaultPorts: `[{"name":"game","port":2456,"protocol":"udp"},{"name":"query","port":2457,"protocol":"udp"}]`,
			defaultEnv: `[
				{"key":"SERVER_NAME","default":"Gamejanitor Valheim Server","label":"Server Name"},
				{"key":"WORLD_NAME","default":"Gamejanitor","label":"World Name"},
				{"key":"SERVER_PASSWORD","default":"changeme","label":"Server Password"},
				{"key":"GAME_PORT","default":"2456","system":true},
				{"key":"QUERY_PORT","default":"2457","system":true},
				{"key":"SAVE_TIMEOUT_SECONDS","default":"15","system":true}
			]`,
			minMemoryMB:          4096,
			minCPU:               2.0,
			gsqGameSlug:          "valheim",
			disabledCapabilities: `["command"]`,
		},
	}

	for _, g := range games {
		result, err := db.Exec(
			`INSERT OR IGNORE INTO games (id, name, image, icon_path, grid_path, hero_path, default_ports, default_env, min_memory_mb, min_cpu, gsq_game_slug, disabled_capabilities) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			g.id, g.name, g.image, g.iconPath, g.gridPath, g.heroPath, g.defaultPorts, g.defaultEnv, g.minMemoryMB, g.minCPU, g.gsqGameSlug, g.disabledCapabilities,
		)
		if err != nil {
			return fmt.Errorf("seeding game %s: %w", g.id, err)
		}

		rows, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("checking rows affected for game %s: %w", g.id, err)
		}
		if rows > 0 {
			slog.Info("seeded game", "id", g.id, "name", g.name)
		} else {
			slog.Debug("game already exists, skipping seed", "id", g.id)
		}
	}

	return nil
}
