.DEFAULT_GOAL := help
.NOTPARALLEL:
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
TEST ?= t1
SCHED ?= tasks
COUNT ?= $(if $(filter t0,$(TEST)),1,100)
PORT ?=
HOLD ?= 1s
TIMEOUT ?= 5s
INTERVAL ?= 100ms
ENUM_TIMEOUT ?= 20
ARGS ?=

# New cases reject undocumented command-line Make variables instead of ignoring them.
USER_VARIABLES := $(foreach key,$(.VARIABLES),$(if $(filter command line,$(origin $(key))),$(key)))
export CDCTEST_UNKNOWN_PARAMS := $(filter-out TEST SCHED COUNT PORT HOLD TIMEOUT INTERVAL ENUM_TIMEOUT ARGS GOCACHE XDG_CACHE_HOME,$(USER_VARIABLES))

# Export values rather than interpolating user input into shell commands.
export CDCTEST_ARGS := $(ARGS)
export CDCTEST_EXPLICIT_PARAMS := $(foreach key,HOLD TIMEOUT INTERVAL,$(if $(filter command line environment override,$(origin $(key))),$(key)))
export CDCTEST_TEST := $(TEST)
export CDCTEST_SCHED := $(SCHED)
export CDCTEST_COUNT := $(COUNT)
export CDCTEST_PORT := $(PORT)
export CDCTEST_HOLD := $(HOLD)
export CDCTEST_TIMEOUT := $(TIMEOUT)
export CDCTEST_INTERVAL := $(INTERVAL)
export CDCTEST_ENUM_TIMEOUT := $(ENUM_TIMEOUT)

.PHONY: help build flash run test check
help build flash run test check:
	@python3 "$(ROOT)/scripts/cdctest.py" $@
