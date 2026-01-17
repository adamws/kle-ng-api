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
pytest --backend-test-host=http://localhost:8080
```
