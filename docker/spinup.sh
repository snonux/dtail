#!/bin/bash

declare -i NUM_INSTANCES=$1
declare -i BASE_PORT=2222
declare -r SERVERLIST=serverlist.txt

for (( i=0; i < $NUM_INSTANCES; i++ )); do
    port=$[ BASE_PORT + i + 1 ]
    name=dserver-serv$i
    echo Creating $name
    docker run -d --name $name --hostname serv$i -p $port:2222 dserver:develop
    echo localhost:$port >> $SERVERLIST.tmp
done

mv $SERVERLIST.tmp $SERVERLIST
