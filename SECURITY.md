# Politique de sécurité

## Signaler une vulnérabilité

Utilisez les « Private vulnerability reports » GitHub de ce dépôt. Ne pas ouvrir d'issue publique.
Réponse sous 7 jours, correctif ou mitigation visés sous 30 jours.

## Versions supportées

Seule la dernière version mineure publiée reçoit des correctifs.

## Vérifier une image

```sh
cosign verify ghcr.io/gabltz/fugace:<version> \
  --certificate-identity-regexp 'https://github.com/gabltz/fugace/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## Garanties et limites

Voir `docs/THREAT_MODEL.md`, section « Limites assumées ».
