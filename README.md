# HOGS - Halls of Game Servers

A modern web interface for managing and showcasing game servers (Minecraft, Satisfactory, Factorio, and more). Built with Go, it provides a clean UI for server status, mod downloads, and map viewing, protected by OIDC authentication for administrative tasks.

## Features

*   **Multi-Game Support:** Query Minecraft, Satisfactory, Factorio, and Valheim servers with game-specific status protocols. New games can be added via the querier registry.
*   **Real-time Server Status:** Live player counts, version info, and online status using efficient caching (60s TTL).
*   **Role-Based Access Control:** OIDC groups map to admin/user roles. Admins get full dashboard access; users can interact with servers based on per-server permissions.
*   **Pterodactyl Integration:** Link servers to a Pterodactyl panel for start/stop/restart, whitelist management, and approved command execution — all configurable per-server.
*   **Admin Dashboard:** Complete web-based management interface for adding, editing, and deleting servers without touching the database.
*   **Background Images:** Customizable, theme-aware background images with hash-addressed caching for performance.
*   **File Browser:** Automatically scans and serves mod files, modpacks, and documentation from a structured directory.
*   **Map Proxy:** Securely proxies web map instances (e.g., BlueMap for Minecraft) through the main web server.
*   **OIDC Authentication:** Secure login via OpenID Connect (e.g., Keycloak, Google) for administrative access.
*   **Modern Architecture:**
    *   **Backend:** Go (1.24+) with `gorilla/mux` and `database/sql`.
    *   **Database:** SQLite with `golang-migrate` for robust schema management.
    *   **Frontend:** Server-side rendered HTML (Go templates) with Bootstrap 5.
    *   **Config:** 12-factor app design using environment variables.
    *   **Auth:** OIDC via `go-oidc` with `gorilla/sessions`.
    *   **Container:** Podman/Docker via Containerfile.

## Getting Started

### Prerequisites

*   **Go:** Version 1.24 or higher.
*   **SQLite:** (Optional) For inspecting the database manually.
*   **GCC:** Required for `go-sqlite3` (CGO).

### Installation

1.  Clone the repository:
    ```bash
    git clone https://github.com/tionis/hogs.git
    cd hogs
    ```

2.  Build the application:
    ```bash
    go build -o hogs .
    ```

### Containers (Docker/Podman)

#### Pre-built Image
We automatically build a container image for the `main` branch, available at `ghcr.io/tionis/hogs:latest`.

#### Run with Podman
Here is an example of how to run the application using Podman, persisting data and game files:

```bash
podman run -d --name hogs \
    -p 8080:8080 \
    -v ./data:/data \
    -v ./game:/app/data/game \
    -e SESSION_SECRET="change-this-to-a-long-random-string" \
    -e OIDC_PROVIDER_URL="" \
    -e OIDC_CLIENT_ID="" \
    -e OIDC_CLIENT_SECRET="" \
    ghcr.io/tionis/hogs:latest
```
*Note: Refer to the [Configuration Reference](#configuration-reference) below for OIDC details required for admin access.*

#### Build Manually
A `Containerfile` is provided for building a container image manually:
```bash
podman build -t hogs .
podman run -p 8080:8080 -v ./data:/data hogs
```

### Running the Application

1.  **Set Environment Variables:**
    Create a `.env` file or export these variables:
    ```bash
    export PORT=8080
    export DB_PATH=./hogs.db
    export GAME_DATA_PATH=./data/game
    # OIDC Config (Optional - Login disabled if missing)
    export OIDC_PROVIDER_URL=https://auth.example.com/realms/ieee
    export OIDC_CLIENT_ID=hogs
    export OIDC_CLIENT_SECRET=your-secret
    export OIDC_REDIRECT_URL=http://localhost:8080/auth/callback
    export SESSION_SECRET=change-this-to-a-long-random-string
    ```

2.  **Run the binary:**
    ```bash
    ./hogs
    ```
    Or directly with Go:
    ```bash
    go run .
    ```

3.  Access the UI at `http://localhost:8080`.
    *   **Admin Login:** Go to `/login` to authenticate via OIDC (if configured).
    *   **Admin Dashboard:** Go to `/admin` to manage servers.

### Helper Scripts
The repository includes helper scripts for development/testing:
*   `go run insert_dummy_data.go`: Populates the database with sample servers.
*   `go run update_map_url.go`: Example script to update database records programmatically.

## Configuration Reference

| Variable               | Default                         | Description                                                                 |
| ---------------------- | ------------------------------- | --------------------------------------------------------------------------- |
| `PORT`                 | `8080`                          | The HTTP port to listen on.                                                 |
| `DB_PATH`              | `./hogs.db`                     | Path to the SQLite database file. Created automatically if missing.         |
| `GAME_DATA_PATH`       | `data/game`                     | Root directory for storing server mod/game files and backgrounds.            |
| `OIDC_PROVIDER_URL`    | *(Empty)*                       | The OIDC Issuer URL (e.g., Keycloak realm URL). Login disabled if empty.    |
| `OIDC_CLIENT_ID`       | *(Empty)*                       | The Client ID registered with your IDP.                                     |
| `OIDC_CLIENT_SECRET`   | *(Empty)*                       | The Client Secret for the application.                                      |
| `OIDC_REDIRECT_URL`    | `.../auth/callback`             | The callback URL whitelisted in your IDP.                                   |
| `SESSION_SECRET`        | `super-secret...`               | Random string used to encrypt session cookies. **Change in production!**    |
| `OIDC_ADMIN_GROUP`     | `admins`                        | OIDC group claim value that grants the admin role.                           |
| `OIDC_USER_GROUP`      | *(Empty)*                       | OIDC group claim value that grants the user role. Empty = any authenticated user is a user. |
| `OIDC_GROUPS_CLAIM`    | `groups`                        | The OIDC claim path to extract group memberships from.                     |
| `PTERODACTYL_URL`      | *(Empty)*                       | Pterodactyl panel URL (e.g. `https://panel.example.com`). Empty = disabled. |
| `PTERODACTYL_APP_KEY`  | *(Empty)*                       | Pterodactyl Application API key. Required if `PTERODACTYL_URL` is set.      |

## Usage Guide

### 1. Managing Servers
Servers are managed via the web-based Admin Dashboard at `/admin`.
1.  **Log in:** Authenticate via OIDC to access the dashboard.
2.  **Add/Edit:** Use the interface to configure server details:
    *   **Name:** Unique identifier (used in URLs and file paths).
    *   **Address:** The server address (e.g., `mc.example.com:25565`).
    *   **Game Type:** `minecraft`, `satisfactory`, or `factorio`.
    *   **State:** Controls visibility (`online`, `offline`, `planned`, `maintenance`).
    *   **Map URL:** Internal URL for proxying maps (e.g., BlueMap for Minecraft).
    *   **Mod Pack URL:** Optional direct download link.
    *   **Metadata:** Custom key-value pairs. Satisfactory: add `api_token`; Factorio: add `rcon_password`.

### 2. Managing Files
You can manage mod/game files via the **Admin File Manager** or directly on the filesystem.

#### Via File Manager
Click "Files" on any server in the Admin Dashboard to upload, delete, or organize files directly from the browser.

#### Manual Organization
The application serves files from `GAME_DATA_PATH` (default: `data/game`).
Directory structure must match the **server name**:

```text
data/game/
├── Creative/               <-- Matches server name "Creative"
│   ├── mods/
│   │   ├── sodium.jar
│   │   └── iris.jar
│   ├── modpack-v1.zip
│   ├── rules.md            <-- Rendered as text/markdown
│   └── discord.url         <-- Rendered as a link
└── Survival/
    └── ...
```

*   **`.md` files:** Content is displayed in the file browser.
*   **`.url` files:** Rendered as external links.
*   **Other files:** Served as direct downloads.

### 3. Map Proxy
To enable the map proxy:
1.  Ensure your map backend is running (e.g., BlueMap at internal IP `10.0.0.5:8100`).
2.  In the Admin Dashboard, set the **Map URL** for the server to `http://10.0.0.5:8100`.
3.  The map will be accessible publicly at `http://your-site.com/Creative/map/`.

### 4. Game-Specific Query Setup

#### Minecraft
Uses the standard Minecraft query protocol (port 25565). No additional configuration needed. Optionally configure BlueMap URL for map proxy.

#### Satisfactory
Queries the Satisfactory Dedicated Server REST API. Add `api_token` to server metadata with your API bearer token. Set the address to `host:api_port` (default API port is 15777).

#### Factorio
Uses RCON to query the Factorio server. Add `rcon_password` to server metadata with your RCON password. Set the address to `host:rcon_port`. Without an RCON password, only basic TCP connectivity check is performed.

#### Valheim
Uses the Steam A2S query protocol. Set the address to `host:port` (default query port is 2457). No additional metadata required.

## Architecture

*   **`main.go`**: Entry point. Wires dependencies (Config, Store, Auth, Pterodactyl) and starts the server.
*   **`api/`**: API handlers (`ServerHandler`, `PterodactylHandler`) for JSON endpoints, proxy logic, and Pterodactyl integration.
*   **`web/`**: `WebHandler` and embedded HTML `templates/`.
*   **`auth/`**: OIDC authentication with role-based access control (admin/user from OIDC groups).
*   **`database/`**: SQLite repository pattern and schema migrations (embedded).
*   **`query/`**: Game querier interface with registry pattern (Minecraft, Satisfactory, Factorio, Valheim).
*   **`pterodactyl/`**: Pterodactyl Application API client for server power actions and commands.
*   **`modmanager/`**: Secure filesystem scanning for mod/game files.
*   **`config/`**: Environment variable loading.

## API Reference

The application exposes several JSON endpoints:

*   `GET /api/servers`: Returns a list of all visible servers.
*   `GET /api/servers/{serverName}/status`: Returns real-time status (online/offline, players) for a server.
*   `GET /api/servers/{serverName}/mods`: Returns the file tree of mods for a server.
*   `GET /files/{serverName}/mods/...`: Downloads a file directly.

## Development

### Running Migrations
Migrations run automatically on startup. To add a new migration:
1.  Create a pair of files in `database/migrations/`:
    *   `XXXXXX_description.up.sql`
    *   `XXXXXX_description.down.sql`
2.  Rebuild the application (migrations are embedded).

### Running Tests
To run the test suite:
```bash
go test ./...
```
To run tests with coverage:
```bash
go test -cover ./...
```

### License
[MIT License](LICENSE)
