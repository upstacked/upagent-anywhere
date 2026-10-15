{
  description = "Install and keep up the machines that run upstacked-agent";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (pkgs: rec {
        upagent-anywhere = pkgs.buildGoModule {
          pname = "upagent-anywhere";
          version = self.shortRev or "dirty";
          src = ./.;
          subPackages = [ "cmd/upagent-anywhere" ];
          vendorHash = "sha256-clJqc5E8L4sv9eujlTxYVSZXE/Twr8WcroYVFWaT+as=";
          ldflags = [ "-X main.version=${self.shortRev or "dirty"}"
            "-X github.com/upstacked/upagent-anywhere/internal/cli.Version=${self.shortRev or "dirty"}" ];
          meta.mainProgram = "upagent-anywhere";
        };
        default = upagent-anywhere;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go pkgs.gopls ]; };
      });
    };
}
