#!/bin/bash
CUR_DIR="$(dirname "$0")"
FLAG_FILE="./es_restart_request"
cd "$CUR_DIR/Grout-Save-Sync" || exit 1

export CFW=ROCKNIX
export LD_LIBRARY_PATH="$CUR_DIR/Grout-Save-Sync/lib:$LD_LIBRARY_PATH"
#export FLIP_FACE_BUTTONS=1
chmod +x ./grout

./grout

if [ -f "$FLAG_FILE" ]; then
    rm -f "$FLAG_FILE"
    killall emulationstation
    # or `systemctl restart essway`
fi

exit 0
