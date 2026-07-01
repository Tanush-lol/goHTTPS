# Dev shell for building the Gio (gioui.org) server GUI on Wayland.
# Enter with:  nix-shell
{ pkgs ? import <nixpkgs> {} }:

pkgs.mkShell {
  buildInputs = [
    pkgs.go
    pkgs.gcc
    pkgs.pkg-config

    # Wayland + input
    pkgs.wayland
    pkgs.wayland-protocols
    pkgs.wayland-scanner
    pkgs.libxkbcommon

    # Rendering backends Gio needs (OpenGL ES / EGL / Vulkan)
    pkgs.libGL
    pkgs.vulkan-headers
    pkgs.vulkan-loader

    # X11 libs so the same binary also runs under Xorg/XWayland
    pkgs.xorg.libX11
    pkgs.xorg.libXcursor
    pkgs.xorg.libXfixes
    pkgs.xorg.libxcb
  ];

  # Gio dlopens these at runtime; make sure they are on the loader path.
  LD_LIBRARY_PATH = pkgs.lib.makeLibraryPath [
    pkgs.wayland
    pkgs.libxkbcommon
    pkgs.libGL
    pkgs.vulkan-loader
    pkgs.xorg.libX11
    pkgs.xorg.libXcursor
    pkgs.xorg.libXfixes
    pkgs.xorg.libxcb
  ];
}
