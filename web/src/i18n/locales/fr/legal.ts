/**
 * 法律 / 信息页词条（Français）：用户协议、隐私政策、安全致谢、联系方式、
 * 加入交流群、投诉举报、安装向导七个页面共用本域，键按页面分组。
 *
 * 意图（Why）：
 *   七个合规 / 信息页文案量大（条款正文属法律文案）且更新节奏独立，单独成域
 *   便于集中审校与翻译；各语言必须完整、正式，保持条款编号与结构不变。
 *
 * 流转（Flow）：
 *   ./legal.ts → locales/<lang>/index.ts 聚合到 site.legal → 页面 t('site.legal.<页面>.*')
 *
 * 扩展（Extend）：
 *   新增页面分组：六个语言的 legal.ts 同步加同名分组（键集合必须一致）；
 *   新增条款：保持 s<N>Title / s<N>Body 编号连续，六语言同步补齐。
 */
export default {
  terms: {
    title: 'Conditions d’utilisation',
    updatedAt: 'Dernière mise à jour : 2026-09-30',
    s1Title: '1. Dispositions générales',
    s1Body:
      'Ce service est un service de relais d’API LLM fourni par l’exploitant de ce site. En l’utilisant, vous confirmez avoir lu et accepté l’intégralité des conditions du présent accord ; si vous n’êtes pas d’accord, cessez d’utiliser le service.',
    s2Title: '2. Compte et sécurité',
    s2Body:
      'Vous devez conserver en sécurité votre compte et vos jetons d’accès, et ne pas les prêter, les céder ni en faire un usage malveillant. Vous assumez les conséquences d’une mauvaise conservation. Si vous constatez que votre compte a été compromis, contactez immédiatement un administrateur.',
    s3Title: '3. Solde et facturation',
    s3Body:
      'Les soldes sont libellés en RMB ; les rechargements servent à compenser les frais d’appel d’API et ne constituent ni une monnaie virtuelle ni un droit cessible. Les soldes sont réservés à votre usage personnel et ne peuvent être cédés ni retirés. Les règles de facturation suivent la place du marché publiée, et toute modification tarifaire sera annoncée au préalable sur le site.',
    s4Title: '4. Déclaration relative aux capacités en amont fondées sur des comptes d’abonnement',
    s4Body1:
      'Ce site peut se connecter à des capacités en amont authentifiées par des comptes d’abonnement tiers, afin d’élargir les modèles et les routes disponibles. Ces capacités ne sont proposées qu’à des fins d’apprentissage, de recherche et de validation technique ; elles complètent les services existants et ne constituent aucun engagement commercial ni aucune autorisation de revente.',
    s4Body2:
      'Lorsque vous utilisez ces capacités, il vous appartient de vous assurer du respect des conditions d’utilisation des fournisseurs en amont concernés et de la législation de votre juridiction ; vous ne devez pas les utiliser pour la revente commerciale, l’extraction massive, le contournement des restrictions ou des quotas en amont, ni pour tout usage sortant du cadre autorisé.',
    s4Body3:
      'Toutes les conséquences découlant de votre manquement aux présentes ou aux conditions des fournisseurs en amont (y compris, sans s’y limiter, la restriction de comptes en amont, l’interruption de service, des litiges ou une responsabilité juridique) sont à votre charge, sans responsabilité solidaire du site. Le site ne fournit qu’un accès technique et n’offre aucune garantie quant à la disponibilité ou à la conformité des comptes en amont.',
    s4Body4:
      'Le site se réserve le droit de suspendre ou de résilier les services concernés et de restreindre ou de désactiver les comptes correspondants en cas de plainte d’un fournisseur en amont, d’exigence réglementaire ou de constatation d’un usage abusif.',
    s5Title: '5. Conformité des contenus',
    s5Body:
      'Vous ne devez pas générer ni diffuser de contenus illégaux ou non conformes via ce service (y compris, sans s’y limiter, la mise en danger de la sécurité nationale, la pornographie ou la violence, ou l’atteinte aux droits d’autrui). Le site dispose de mécanismes de filtrage de mots sensibles et de sécurité des contenus ; les contenus fautifs seront refusés et pourront entraîner des mesures sur le compte.',
    s6Title: '6. Disponibilité du service',
    s6Body:
      'Ce service s’efforce d’assurer un fonctionnement stable mais ne s’engage pas sur une disponibilité absolue. En cas d’indisponibilité due à une panne d’un fournisseur en amont, à des fluctuations réseau ou à une maintenance planifiée, le site fera de son mieux pour rétablir le service mais n’assume pas les pertes indirectes qui en découlent.',
    s7Title: '7. Clause de non-responsabilité',
    s7Body:
      'Le site n’assume aucune responsabilité pour les pertes causées par un cas de force majeure, un piratage, une panne système, une interruption de service tiers ou toute autre cause qui ne lui est pas imputable. Voir la clause de non-responsabilité complète pour un exposé plus détaillé.',
    s8Title: '8. Modification de l’accord',
    s8Body:
      'Le site peut réviser cet accord selon ses besoins d’exploitation ; toute révision sera publiée sur le site. La poursuite de l’utilisation vaut acceptation des conditions révisées.',
  },
  privacy: {
    title: 'Politique de confidentialité',
    updatedAt: 'Dernière mise à jour : 2026-09-30',
    s1Title: '1. Quelles informations nous collectons',
    s1Body:
      'Les informations de compte recueillies à l’inscription (nom d’utilisateur, e-mail, hachage du mot de passe) ; le contenu des requêtes et les informations d’usage dans les journaux d’appel ; ainsi que les informations nécessaires sur l’appareil et le réseau (pour la sécurité et la gestion des risques).',
    s2Title: '2. Utilisation des informations',
    s2Body:
      'Pour fournir et maintenir le service, gérer la facturation, dépanner, assurer la sécurité et effectuer des audits de conformité. Nous ne vendons ni ne louons vos informations personnelles à des tiers.',
    s3Title: '3. Stockage des informations',
    s3Body:
      'Les données sont stockées sur les serveurs propres à ce site (stockage local SQLite par défaut). Les mots de passe sont conservés sous forme de hachages salés, jamais en clair. Les jetons d’accès en clair ne sont affichés qu’une seule fois, à leur création.',
    s4Title: '4. Vos droits',
    s4Body:
      'Vous avez le droit d’accéder à vos informations de compte et données associées, de les corriger ou de les supprimer. Pour supprimer votre compte, veuillez contacter un administrateur. Conformité des contenus : pour les requêtes contraires à la loi, nous les conserverons comme l’exige la loi et coopérerons avec les autorités de régulation.',
    s5Title: '5. Protection des mineurs',
    s5Body:
      'Ce service n’est pas destiné aux mineurs de moins de 18 ans. Si vous êtes tuteur et constatez qu’un mineur utilise ce service, veuillez nous contacter.',
    s6Title: '6. Modification de cette politique',
    s6Body:
      'En cas de modification substantielle, cette politique sera publiée de manière visible sur le site. La poursuite de l’utilisation vaut acceptation de la politique mise à jour.',
  },
  security: {
    title: 'Remerciements sécurité',
    updatedAt: 'Dernière mise à jour : 2026-09-30',
    s1Title: 'Signaler une vulnérabilité',
    s1Body:
      'Si vous découvrez une vulnérabilité de sécurité sur ce site, nous vous encourageons à la divulguer de manière responsable : ne diffusez pas publiquement les détails ; contactez d’abord un administrateur par e-mail ou par le groupe, et nous vous remercierons publiquement après la correction.',
    s2Title: 'Contenu du rapport',
    s2Body:
      'Veuillez décrire : le type de vulnérabilité, son impact, les étapes de reproduction (aussi concises que possible) et la correction suggérée. Ne réalisez pas de tests destructifs.',
    s3Title: 'Liste de remerciements',
    s3Body: 'Nous remercions les chercheurs suivants pour leur contribution à la sécurité de ce site :',
    thanksRevealed: '(La liste sera publiée ici dès réception de rapports valides)',
    thanksShow: 'Voir les chercheurs remerciés',
  },
  contact: {
    title: 'Nous contacter',
    updatedAt: 'Dernière mise à jour : 2026-09-30',
    emailTitle: 'E-mail du support',
    emailBody: 'Pour toute demande commerciale, partenariat ou assistance, écrivez-nous à :',
    emailNotSet: '(Aucune adresse publique configurée pour le moment ; contactez-nous via le groupe communautaire)',
    groupTitle: 'Groupe communautaire',
    groupBodyPrefix: 'En cas de problème d’utilisation, rejoignez le groupe communautaire pour obtenir de l’aide : ',
    groupLink: 'Rejoindre la communauté',
    groupBodySuffix: '.',
    hoursTitle: 'Délais de réponse',
    hoursBody:
      'Nous nous efforçons de répondre aux e-mails sous 48 heures. Pour les urgences (comme un compte compromis), prévenez aussi un administrateur via le groupe communautaire.',
  },
  join: {
    title: 'Rejoindre la communauté',
    subtitle: 'Une question, une demande de fonctionnalité, ou simplement envie de parler de modèles — venez nous rejoindre.',
    groupMainName: 'Groupe principal LTZY-API',
    groupMainDesc: 'Discussions sur l’usage, l’intégration des canaux et les aperçus des nouveautés.',
    joinButton: 'Rejoindre',
  },
  report: {
    title: 'Signaler un abus',
    updatedAt: 'Dernière mise à jour : 2026-09-30',
    s1Title: 'Champ d’application',
    s1Body:
      '1) Plaintes relatives à la qualité du service de ce site ; 2) contenus publiés par ce site ou ses utilisateurs soupçonnés d’être illégaux ou non conformes ; 3) problèmes de sécurité tels que des comptes compromis ou détournés.',
    s2Title: 'Comment signaler',
    s2BodyPrefix: 'Veuillez adresser vos signalements par e-mail :',
    emailNotSet: '(Aucune adresse de signalement publique configurée pour le moment)',
    s2BodySuffix:
      '. Veuillez joindre si possible les éléments pertinents (captures d’écran, horaires, contenu concerné) afin que nous puissions vérifier rapidement.',
    s3Title: 'Délai de traitement',
    s3Body:
      'Nous nous engageons à répondre aux signalements sous 48 heures et, après vérification, à prendre les mesures nécessaires prévues par la loi (suppression de contenu, traitement du compte, signalement aux autorités, etc.).',
    s4Title: 'Responsabilité en cas de fausse déclaration',
    s4Body: 'Nous nous réservons le droit d’engager des poursuites pour tout signalement délibérément fabriqué ou malveillant.',
  },
  install: {
    checking: 'Vérification de l’état d’installation…',
    doneTitle: 'Installation terminée',
    doneDesc: 'Un compte administrateur a été configuré. Veuillez vous connecter au panneau d’administration.',
    doneButton: 'Accéder à la connexion admin',
    wizardBrand: 'Assistant d’installation',
    title: 'Créer un administrateur',
    subtitle: 'Il s’agit de la première installation ; veuillez configurer le compte administrateur du site',
    siteNameLabel: 'Nom du site',
    siteNameHelp: 'Affiché dans le titre de la page et le pied de page',
    siteNamePlaceholder: 'ex. : Passerelle LTZY-API',
    usernameLabel: 'Nom d’utilisateur administrateur',
    passwordLabel: 'Mot de passe administrateur',
    passwordHelp: 'Au moins 8 caractères',
    passwordPlaceholder: 'Au moins 8 caractères',
    confirmLabel: 'Confirmer le mot de passe',
    confirmPlaceholder: 'Saisissez à nouveau',
    submit: 'Terminer l’installation',
    haveAccount: 'Vous avez déjà un compte ?',
    loginLink: 'Se connecter',
    errPasswordShort: 'Le mot de passe doit comporter au moins 8 caractères',
    errPasswordMismatch: 'Les deux mots de passe ne correspondent pas',
    errFailed: 'Échec de l’installation',
  },
}
