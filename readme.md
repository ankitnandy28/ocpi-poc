# OCPI Proof of Concept

A small Go project that simulates how a charging point operator (CPO) shares charging location data with an e-mobility service provider (eMSP) using the OCPI protocol (I used version 2.3.0).

This is a learning POC, not a production or fully OCPI-compliant system.

## Prerequisites
- made and tested with go version go1.24.1 windows/amd64
- [`swag`](https://github.com/swaggo/swag) — only needed to regenerate the Swagger files

## Components

| Program | Port | Role |
|---|---|---|
| `cmd/ocpi-server` | 8080 | OCPI server. Registers partners and serves OCPI endpoints |
| `cmd/mock-cpo` | 8081 | Mock CPO. Provides fake charging location data |
| `cmd/mock-emsp` | 8083 | Mock eMSP. Registers with the OCPI server and requests locations |

## Quick start

Option A: start everything with one script

```powershell
powershell -ExecutionPolicy Bypass -File .\start-all.ps1
```

Option B: start each program yourself, in separate terminals, in this order:

```powershell
go run .\cmd\ocpi-server
go run .\cmd\mock-cpo
go run .\cmd\mock-emsp
```

Start the OCPI server and Mock CPO before the eMSP. The eMSP needs both.

## Expected output

- OCPI server: `OCPI server running at http://localhost:8080`, then `credentials registered with IN/EMSP`
- Mock CPO: `Mock CPO running at http://localhost:8081`
- Mock eMSP: `registered with CPO`, followed by a registered token and the locations URL

## Swagger documentation

- OCPI server: http://localhost:8080/swagger/index.html
- Mock CPO: http://localhost:8081/swagger/index.html
- Mock eMSP: http://localhost:8083/swagger/index.html

The eMSP also exposes `/emsp/state`, which reports its own registration status. It is deliberately left out of the generated docs.

## Stopping

Press **Ctrl+C** in each program's window.

## Project structure

```text
ocpi-poc/
├── go.mod
├── start-all.ps1              # starts all three programs
├── README.md
├── cmd/
│   ├── ocpi-server/           # OCPI server (port 8080)
│   │   ├── main.go            # OCPI endpoints and routing
│   │   └── main_test.go
│   ├── mock-cpo/              # Mock CPO (port 8081)
│   │   └── main.go            # fake charging location data
│   └── mock-emsp/             # Mock eMSP (port 8083)
│       ├── main.go            # eMSP web server and locations request
│       ├── register.go        # credentials handshake with the OCPI server
│       └── main_test.go
├── internal/
│   ├── config/
│   │   └── config.go          # ports, hosts and base URLs
│   └── credentials/
│       └── store.go           # OCPI credentials module and token store
└── docs/                      # generated Swagger files
```

- `cmd/` holds the three programs. Each one has its own `main.go` and runs separately.
- `internal/` holds shared code that the programs import.
- `docs/` holds the Swagger files that `swag init` generates.


## Limitations

- Data is stored in memory. Restarting the OCPI server clears all registrations.
- Credentials and tokens are fixed demo values, so they are not secure.
- Only a small part of OCPI is implemented — locations and credentials only; sessions, CDRs, and commands are not done yet.
- Port numbers are set in `internal/config/config.go`.
- If you see `bind: Only one usage of each socket address`, a previous program is still running on that port. Close it and try again.
