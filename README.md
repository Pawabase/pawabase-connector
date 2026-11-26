# pawabase-connector

> **Beta.** No release has been published yet; the install commands below work once `v0.1.0` is out. Until then, build it from source
> (`go build ./cmd/pawabase-connector`).

Use your own Postgres database with a Pawabase project **without giving Pawabase its password**.

The connector runs next to your database. It dials out to Pawabase (you open no port), logs into the database itself with an address
only it knows, and answers Pawabase's connections through that one encrypted tunnel. Pawabase keeps no copy of your database's address,
user or password, and you can cut it off at any moment by stopping the connector or removing it in Studio.

```
 your network                                       Pawabase
┌───────────────────────────────┐            ┌───────────────────────────┐
│ Postgres ◄── pawabase-connector│  outbound  │ your project's API, flows, │
│ (5432 stays     (holds the    │──TLS/WSS──►│ Studio — reading and       │
│  private)        password)    │   tunnel   │ writing through the tunnel │
└───────────────────────────────┘            └───────────────────────────┘
```

## Install

**macOS and Linux**

```sh
curl -fsSL https://raw.githubusercontent.com/Pawabase/pawabase-connector/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/Pawabase/pawabase-connector/main/install.ps1 | iex
```

**Docker**

```sh
docker run -d --name pawabase-connector --restart unless-stopped \
  -v pawabase-connector:/var/lib/pawabase-connector \
  -e PAWABASE_SERVER=https://api.your-project.example \
  -e PAWABASE_CONNECTOR_TOKEN=pbce_… \
  -e DATABASE_URL='postgres://user:password@db:5432/app' \
  ghcr.io/pawabase/pawabase-connector:latest
```

Or download an archive for your system from [Releases](https://github.com/Pawabase/pawabase-connector/releases) and check it against
`checksums.txt`. Builds: Linux, macOS and Windows, on x86-64 and ARM64.

## Connect a project

1. In Studio, open **Project settings → Database → Bring your own database** and make a connector. Studio shows a one-time token
   (`pbce_…`, valid for 24 hours) and these commands.
2. On the machine next to your database:

   ```sh
   sudo pawabase-connector enroll --server https://api.your-project.example --token pbce_…
   sudo pawabase-connector service install
   ```

   `enroll` asks for the database address (hidden), checks that it can log in, and exchanges the token for this connector's own key.
   `service install` runs it in the background and starts it with the machine (systemd on Linux, launchd on macOS, a Windows service).
   On Windows, run both in a PowerShell window opened as Administrator.
3. When Studio shows the connector online, choose **Use this database**. Your environments read and write it from then on.

## The database role

Give the connector a role of its own, not a superuser. Pawabase keeps each environment in its own schema, so the role needs to
create schemas and tables in the database:

```sql
CREATE ROLE pawabase LOGIN PASSWORD '…';
CREATE DATABASE app OWNER pawabase;          -- or, on an existing database:
GRANT CREATE, CONNECT ON DATABASE app TO pawabase;
```

Pawabase can do whatever this role may do in that database, and nothing more.

## Commands

| Command | What it does |
| --- | --- |
| `pawabase-connector enroll --server URL --token TOKEN [--database-url URL] [--name NAME]` | Exchange the one-time token for this connector's key and save the configuration. |
| `pawabase-connector run` | Run in the foreground (logs to the terminal). |
| `pawabase-connector status` | Show the configuration, and check the database, Pawabase and the service. |
| `pawabase-connector service install` · `uninstall` · `start` · `stop` · `restart` · `status` | Manage the background service. |
| `pawabase-connector version` | Print the version. |

## Configuration

The configuration (Pawabase's address, the connector's key and the database address) is one file, readable only by its owner:

| Run as | File |
| --- | --- |
| root / Administrator (the service) | `/etc/pawabase-connector/config.json` (Linux), `/Library/Application Support/pawabase-connector/config.json` (macOS), `%ProgramData%\pawabase-connector\config.json` (Windows) |
| a normal user | your configuration folder, `pawabase-connector/config.json` |
| anywhere | the file named by `PAWABASE_CONNECTOR_CONFIG` |

`DATABASE_URL` (or `PAWABASE_CONNECTOR_DATABASE_URL`) in the environment overrides the saved address, so it can come from your
secret manager instead of the file. The service writes its log to `connector.log` next to the configuration.

## How it works

The connector keeps one WebSocket to `wss://<your Pawabase>/connect/v1/tunnel`, authenticated with its key. Pawabase opens a stream on
it for every database connection it needs. For each stream the connector speaks the Postgres protocol as a server: the client must give
the tunnel's per-session secret, then the connector logs into your database and passes the conversation through. If the tunnel drops,
the connector reconnects on its own and Pawabase answers requests for the project with a clear "connector offline" error until it does.
Removing the connector in Studio revokes its key at once.

## Building

```sh
go build ./cmd/pawabase-connector
go test ./...
```

Releases are made by pushing a `v*` tag: GoReleaser builds every platform and the container image is pushed to
`ghcr.io/pawabase/pawabase-connector`.

## License

BSD 3-Clause.
