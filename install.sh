#!/bin/bash
# Command line install alternative to the UI
echo "Please enter your SPR path (/home/spr/super/)"
read -r SUPERDIR

if [ -z "$SUPERDIR" ]; then
    SUPERDIR="/home/spr/super/"
fi

export SUPERDIR

echo "Please enter your SPR API token:"
read -r SPR_API_TOKEN

if [ -z "$SPR_API_TOKEN" ]; then
  echo "need api token, generate one on the auth keys page"
  exit 1
fi

mkdir -p "$SUPERDIR/configs/plugins/spr-tunwg"

# Token used only for registering the plugin interface with the SPR firewall
# below; the backend itself never calls the SPR API.
printf '%s' "$SPR_API_TOKEN" > "$SUPERDIR/configs/plugins/spr-tunwg/api-token"
chmod 600 "$SUPERDIR/configs/plugins/spr-tunwg/api-token"

if [ ! -f "$SUPERDIR/configs/plugins/spr-tunwg/config.json" ]; then
  echo '{"Forwards":[]}' > "$SUPERDIR/configs/plugins/spr-tunwg/config.json"
  chmod 600 "$SUPERDIR/configs/plugins/spr-tunwg/config.json"
fi

docker compose build
docker compose up -d
CONTAINER_IP=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "spr-tunwg")
API=127.0.0.1

curl "http://${API}/firewall/custom_interface" \
-H "Authorization: Bearer ${SPR_API_TOKEN}" \
-X 'PUT' \
--data-raw "{\"SrcIP\":\"${CONTAINER_IP}\",\"Interface\":\"spr-tunwg\",\"Policies\":[\"lan\",\"wan\"]}"

docker compose restart
