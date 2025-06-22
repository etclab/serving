#!/bin/bash 

# usage: # ./build-function.sh <scaffold-dir> <function-dir>
# ./build-function.sh ./scaffold ./appender

# find script dir
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SCAFFOLD_DIR="$1"
FUNCTION_DIR="$2"

# ensure <scaffold-dir>/f exists
mkdir -p $SCAFFOLD_DIR/f

# copy everything inside appender dir to scaffold/f dir
cp -r $FUNCTION_DIR/* $SCAFFOLD_DIR/f/

# run the docker build command
cd $SCAFFOLD_DIR
DOCKER_BUILDKIT=1 docker build --secret id=signingkey,src=../private.pem --target deploy -t "atosh502/appender-ego" --push .
cd -