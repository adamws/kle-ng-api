## Tests

### Start Services

From the either `backend` directory, run:

```bash
# Build and start all services
docker compose --profile monitor up --build --watch
```

This starts:
- **backend server** at `http://localhost:8080`
- all other backend services (worker, redis, seaweedfs)

### Run Tests

Set up Python environment and run pytest:

```bash
python -m venv .env
. .env/bin/activate
pip install -r requirements.txt
python -m pytest --backend-test-host=http://localhost:8080
```

The track preset tests run `kicad-cli pcb drc` on the generated board. By default
it runs in the worker image (`--drc-image`, defaults to
`admwscki/keyboard-tools-kicad:$TAG` with `TAG` defaulting to `latest`), so DRC
uses the worker's KiCad version. The board is piped through stdin, so it works
with any reachable docker daemon. To use a local KiCad instead:

```bash
python -m pytest --backend-test-host=http://localhost:8080 --kicad-cli kicad-cli
```
