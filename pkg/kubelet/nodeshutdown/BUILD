load("@io_bazel_rules_go//go:def.bzl", "go_library")

go_library(
    name = "go_default_library",
    srcs = ["nodeshutdown_manager.go"],
    importpath = "k8s.io/kubernetes/pkg/kubelet/nodeshutdown",
    visibility = ["//visibility:public"],
    deps = [
        "//pkg/features:go_default_library",
        "//pkg/kubelet/eviction:go_default_library",
        "//pkg/kubelet/nodeshutdown/systemd:go_default_library",
        "//pkg/kubelet/types:go_default_library",
        "//staging/src/k8s.io/api/core/v1:go_default_library",
        "//staging/src/k8s.io/apiserver/pkg/util/feature:go_default_library",
        "//vendor/github.com/godbus/dbus/v5:go_default_library",
        "//vendor/k8s.io/klog/v2:go_default_library",
        "//vendor/k8s.io/utils/integer:go_default_library",
    ],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [
        ":package-srcs",
        "//pkg/kubelet/nodeshutdown/systemd:all-srcs",
    ],
    tags = ["automanaged"],
    visibility = ["//visibility:public"],
)
