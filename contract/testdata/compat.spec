Name: ghrepo-compat
Epoch: 2
Version: 1.2
Release: 3
Summary: Compatibility summary
Summary(fr): Résumé de compatibilité
License: GPL-3.0-only
BuildArch: noarch
URL: https://example.invalid/ghrepo-compat
Requires: fixture-dependency >= 1.0

%description
Compatibility description.

%description -l fr
Description de compatibilité.

%install
mkdir -p %{buildroot}/usr/share/ghrepo-compat
printf 'fixture\n' > %{buildroot}/usr/share/ghrepo-compat/notes..txt

%files
/usr/share/ghrepo-compat/notes..txt
