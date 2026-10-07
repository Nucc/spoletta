#!/bin/sh
# Placeholder worker: idles in a loop until real work is added here.
trap 'echo "worker: stopping"; exit 0' TERM INT

echo "worker: started"
while true; do
    sleep 30 &
    wait $!
done
