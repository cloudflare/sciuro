load("@bazel_gazelle//:def.bzl", "gazelle")

# gazelle:prefix github.com/cloudflare/sciuro
gazelle(name = "gazelle")

load("@com_github_atlassian_bazel_tools//golangcilint:def.bzl", "golangcilint")

golangcilint(
    name = "golangcilint",
    config = "//:.golangci.yml",
    paths = [
        "cmd/...",
        "internal/...",
    ],
    prefix = "github.com/cloudflare/sciuro",
)
