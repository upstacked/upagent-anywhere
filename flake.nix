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
          vendorHash = "sha256-UdRyylHSZ/b89cEArilaw6LcAw0epmpH6yCOKOiw9Gw=";
          ldflags = [ "-X main.version=${self.shortRev or "dirty"}" ];
          meta.mainProgram = "upagent-anywhere";
        };
        default = upagent-anywhere;
      });

      devShells = forAll (pkgs: {
        default = pkgs.mkShell { packages = [ pkgs.go pkgs.gopls ]; };
      });
    };
}
