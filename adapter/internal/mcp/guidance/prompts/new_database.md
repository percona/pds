---
name: new_database
title: Create a database for an app
description: Create a {{.EngineChoice}} database on one environment, decide who can connect, and wait until it is ready.
access: change
arguments:
  - name: engine
    description: The engine, {{.EngineIDs}}. Asked for when left out.
  - name: environment
    description: The environment to create it on, as list_environments names it. Asked for when left out.
  - name: name
    description: The database name, lowercase letters, digits and dashes. Asked for when left out.
---
Create a database for the user's app on PDS, and wait until it is ready.

1. Engine. {{if .engine}}Use {{.engine}}.{{else}}Ask the user which engine the app needs, {{.EngineChoice}}.{{end}}
2. Environment. {{if .environment}}Use the environment {{.environment}}, and check that list_environments lists it as connected.{{else}}Call list_environments and ask the user which connected environment the database goes on.{{end}}
3. Name. {{if .name}}Call the database {{.name}}.{{else}}Ask the user for a name, or suggest one that fits the app.{{end}}
4. Access. {{if eq .engine "mongodb"}}A MongoDB database has no access from outside the Kubernetes cluster yet, so leave access out and tell the user that only apps inside the Kubernetes cluster can connect.{{else}}Ask where the app will run. If it runs in the same VPC as the environment, use private access. If it runs on a laptop or another host, use public access with that machine's public address as the only allowed range, written as a /32. Ask the user for the address. Never use 0.0.0.0/0. No tool changes access after the database exists, so settle it before you create it.{{if and .MongoDB (not .engine)}} For MongoDB, leave access out: it has no access from outside the Kubernetes cluster yet.{{end}}{{end}}
5. Size. Ask whether this is dev, staging or prod, unless the user has said, and keep to the sizes that profile sets unless they ask for more.
6. Show the user the engine, environment, name, access and size, and ask them to confirm. Then call create_database.
7. Call wait_for_database with until ready. When it ends in progress, call it again, and tell the user what its message says if something blocks the database.
8. When the database is ready, give the user its host and port from get_database. The user copies the password from the console with Copy password. Never show or ask for it.
