#!/bin/bash

which genoptions > /dev/null
if [[ "$?" != "0" ]]; then
	echo "Please install github.com/lestrrat-go/option/cmd/genoptions"
	exit 1
fi

genoptions -objects=options.yaml
