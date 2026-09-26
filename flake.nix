{
  description = "siltide, a terminal monitor for GPUs, NPUs, and other AI accelerators";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      each = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
    in
    {
      packages = each (pkgs: rec {
        siltide = pkgs.buildGoModule {
          pname = "siltide";
          version = self.shortRev or "dev";
          src = self;

          # Update with the hash `nix build` prints when the dependencies change.
          vendorHash = "sha256-9eL08wpFHO7ilvK0CSSutprPaNvoABsdagaRD2P2SIo=";

          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" "-X" "main.version=${self.shortRev or "dev"}" ];

          # The tests that shell out to vendor tools have nothing to talk to in
          # the sandbox; the rest run.
          checkFlags = [ "-skip" "TestFakeNVML" ];

          postInstall = ''
            installShellCompletion --cmd siltide \
              --bash <($out/bin/siltide --completion bash) \
              --zsh <($out/bin/siltide --completion zsh) \
              --fish <($out/bin/siltide --completion fish)
            $out/bin/siltide --man > siltide.1
            installManPage siltide.1
          '';
          nativeBuildInputs = [ pkgs.installShellFiles ];

          meta = {
            description = "Terminal monitor for GPUs, NPUs, and other AI accelerators";
            homepage = "https://github.com/moezdil/siltide";
            license = pkgs.lib.licenses.asl20;
            mainProgram = "siltide";
            platforms = systems;
          };
        };
        default = siltide;
      });

      apps = each (pkgs: rec {
        siltide = {
          type = "app";
          program = "${self.packages.${pkgs.system}.siltide}/bin/siltide";
        };
        default = siltide;
      });

      devShells = each (pkgs: {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.golangci-lint ];
        };
      });
    };
}
