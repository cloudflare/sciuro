
![Sciuro](img/sciuro.png "Sciuro")

* [Introduction](#introduction)
* [Requirements](#requirements)

## Introduction

Sciuro is a bridge between alertmanager and Kubernetes to sync alerts as
Node Conditions. It is designed to work in tandem with other controllers
that observe Node Conditions such as [draino](https://github.com/planetlabs/draino) or the [cluster-api](https://cluster-api.sigs.k8s.io/tasks/healthcheck.html).

## Requirements

* Alertmanager API v2
* Kubernetes 1.12+
