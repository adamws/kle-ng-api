#!/bin/sh

set -o nounset
set -o errexit

if [ -z "$(ssh-keygen -F $SSH_HOST)" ]; then
    echo "Adding host to known hosts"
    ssh-keyscan -H $SSH_HOST >> ~/.ssh/known_hosts 2> /dev/null
fi

SCRIPT_DIR="$(dirname "${0}")"

rsync -avP --exclude="letsencrypt" --exclude=".env" --exclude="failed-tasks" \
  ${SCRIPT_DIR}/ $SSH_USER@$SSH_HOST:/home/$SSH_USER/app

ssh $SSH_USER@$SSH_HOST << EOF
    cd app
    # Worker runs as uid 1000, it must be able to write here
    mkdir -p failed-tasks && chmod 777 failed-tasks
    export TAG=$TAG
    docker compose down -v --rmi="all"
    docker compose pull
    docker compose up -d --force-recreate
EOF
