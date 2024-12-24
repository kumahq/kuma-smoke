TOP = $(shell pwd)

include mk/deps.mk
include mk/check.mk
include mk/build.mk
include mk/run.mk