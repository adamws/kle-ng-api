# Testing deployment locally

Run

``` bash
docker compose up
```

Application will be accessible at `https://kicad.localhost`.
For local runs it is impossible to get trusted certificate so traefik will generate default, self signed certificate.
Your browser will warn you about security risk. This can be safely ignored.

# Deployment on server

In order to deploy on server set environment variables (in `.env` file):

```
ACME_EMAIL=<email to be used for certificate registration>
DOMAIN=<domain poiting to server>
TRAEFIK_CERT_RESOLVER=letsencrypt
```

and run:

```
docker compose up
```

Traefik will also expose other services:

- `https://prometheus.localhost` - default credentials: `user:secret`

# Failed tasks

Workers keep the last `FAILED_TASKS_LIMIT` (default 10) failed tasks in
`FAILED_TASKS_DIR` (see `production.env`), bind-mounted to `~/app/failed-tasks`
on the server. It is a bind mount so it survives `docker compose down -v` in
`deploy.sh`. Older entries are deleted automatically when a new failure is saved.
Unset `FAILED_TASKS_DIR` or set the limit to `0` to disable.
