/**
 * Textes de la page d'accueil (français) : héros / bandeau de spécifications / capacités / démarrage / tarifs des modèles / arbitrages de conception / FAQ / CTA.
 *
 * 意图（Why）：
 *   首页文案量大、改版频繁，从 site.ts 拆出独立文件，便于单独迭代与六语翻译对齐。
 *
 * 流转（Flow）：
 *   ./home.ts → locales/<lang>/index.ts 聚合进 site 命名空间 → i18n/index.ts 的 messages
 *   → app/page.tsx 以 t('site.home.*') 取用（本文件只含 site.home. 之下的层级）。
 *
 * 扩展（Extend）：
 *   新增键时六种语言的同一路径必须同步补齐（六文件键结构完全一致）；
 *   专有名词保留英文：OpenAI / LLM / API / base_url / token / GitHub / MIT。
 */
export default {
  hero: {
    tagline: 'Passerelle LLM auto-hébergée · déploiement en un seul binaire',
    titleLine1: 'Une seule API compatible OpenAI,',
    titleLine2: 'des dizaines de fournisseurs réunis.',
    desc: "Les protocoles OpenAI / Anthropic / Gemini et autres sont ici unifiés au format compatible OpenAI. Facturation, journaux, pools de clés et nouvelles tentatives fonctionnent d'emblée. Toute la passerelle est open source : déployez la vôtre à tout moment.",
    terminalUserMessage: 'Bonjour',
    terminalAssistantReply: 'Bonjour ! Comment puis-je vous aider ?',
  },
  action: {
    console: 'Ouvrir la console',
    register: 'Créer un compte et obtenir un jeton',
    browseModels: 'Parcourir les modèles et tarifs',
    readSource: 'Lire le code source',
  },
  stats: {
    modelsOnline: 'Modèles en ligne',
    channelTypes: 'Types de canaux en amont',
    protocols: 'Protocoles compatibles',
    deployFiles: 'Fichiers à déployer',
  },
  features: {
    title: 'Capacités essentielles',
    desc: "Tout ce qu'une passerelle doit offrir est inclus : conversion de protocoles, facturation, gestion des clés, journalisation et tolérance aux pannes — aucun composant tiers à assembler.",
    f1Title: 'Protocoles unifiés',
    f1Desc: 'Conversion entre les protocoles entrants et sortants OpenAI / Anthropic / Gemini et autres, en n’exposant qu’un seul format compatible OpenAI.',
    f2Title: 'Facturation fine',
    f2Desc: 'Modes au jeton, à l’appel et gratuit, avec des prix configurés par groupe : chaque débit peut être vérifié ligne par ligne.',
    f3Title: 'Pools de clés et nouvelles tentatives',
    f3Desc: 'Plusieurs clés font tourner chaque canal ; en cas d’échec, la suivante prend le relais automatiquement, rendant les hoquets en amont imperceptibles.',
    f4Title: 'Journalisation complète',
    f4Desc: 'Le modèle, le nombre de jetons, la latence et le code d’état de chaque appel sont enregistrés : l’usage reste toujours auditable.',
    f5Title: 'Basculement',
    f5Desc: 'Disjonction au niveau du canal avec temporisation : lorsqu’une route entière tombe, les requêtes réessaient automatiquement sur la suivante.',
    f6Title: 'Auto-hébergement en un seul binaire',
    f6Desc: 'Un binaire Go plus SQLite, avec le frontend intégré : le déploiement ne demande qu’un seul fichier.',
  },
  quickstart: {
    title: 'Démarrage en trois étapes',
    desc: "Seule une API compatible OpenAI est exposée : votre SDK existant n'a besoin que d'une nouvelle base_url.",
    s1Title: 'Créer un compte et un jeton',
    s1Desc: 'Générez un jeton d’accès dans la console et définissez au passage son budget et ses modèles autorisés.',
    s2Title: 'Remplacer la base_url',
    s2Desc: 'Dans tout client prenant en charge le SDK OpenAI, pointez base_url vers le point d’accès /v1 de ce site.',
    s3Title: 'Conserver votre code existant',
    s3Desc: 'Les protocoles sont compatibles : les structures de requête et de réponse sont inchangées et aucun code métier n’est à modifier.',
    sdkNote: '# Python / Node : remplacez la base_url du SDK OpenAI par {base}',
    tokenPlaceholder: 'votre-jeton',
    sampleMessage: 'Bonjour',
  },
  models: {
    title: 'Modèles et tarifs',
    descAgent: 'Vous consultez votre palier revendeur : les prix barrés sont les prix publics, l’orange est votre prix remisé.',
    descPublic: 'Données en direct du site ; les prix sont affichés par groupe, sans embellissement.',
    colModel: 'Modèle',
    colGroup: 'Groupe',
    colPrice: 'Prix',
    viewAllCount: 'Voir les {n} modèles et la grille tarifaire complète',
    viewAll: 'Voir tous les modèles et la grille tarifaire complète',
    priceTbd: 'À définir',
    priceFree: 'Gratuit',
    pricePerCall: "À l'appel",
    pricePerToken: 'Au jeton',
  },
  design: {
    title: 'Choix de conception et coûts',
    desc: 'Pas de boîte noire : règles tarifaires, historique d’usage et limites de coûts sont affichés au grand jour.',
    principlesLabel: 'Principes',
    costsLabel: 'Détail des coûts',
    p1Title: 'Une tarification explicite',
    p1Desc: 'Le prix unitaire de chaque modèle figure sur la place du marché, en modes à l’appel / au jeton / gratuit : chaque facture peut être vérifiée ligne par ligne.',
    p2Title: 'Vous maîtrisez votre usage',
    p2Desc: 'Le modèle, les jetons, la latence et le code d’état de chaque appel sont enregistrés et toujours consultables par vous — et immuables de mon côté.',
    p3Title: 'Le code est open source',
    p3Desc: 'Toute la passerelle est publiée sur GitHub sous licence MIT ; si je m’arrête un jour, vous pourrez toujours déployer la vôtre.',
    c1Label: 'Serveur',
    c1Value: 'Fixe mensuel · un serveur dédié fait tourner la passerelle et la base de données',
    c2Label: 'Bande passante et trafic',
    c2Value: 'Variable · plus il y a d’appels, plus il augmente',
    c3Label: 'Frais des modèles en amont',
    c3Value: 'À l’usage · le prix que je paie en amont sert de base de coût',
    c4Label: 'Domaine et certificat',
    c4Value: 'Un montant modique par an',
    costNotePrefix:
      'Ce que vous payez couvre d’abord les coûts ; seul le surplus justifie que je continue à le maintenir. Si un jour l’équilibre n’est vraiment plus atteignable, je l’expliquerai dans les annonces, ',
    costNoteBold: 'plutôt que d’augmenter les prix en silence',
    costNoteSuffix: '.',
  },
  faq: {
    title: 'Questions fréquentes',
    q1: 'Ce site restera-t-il ouvert durablement ?',
    a1: 'Je ferai de mon mieux. Ses coûts sont maîtrisés, je n’en tire pas de profit et il n’y a pas de problème du type « financer puis disparaître ». Si une fermeture devait survenir, je l’annoncerais à l’avance avec un guide complet d’auto-hébergement.',
    q2: 'Pourquoi l’ouvrir en open source ?',
    a2: 'D’abord pour que vous puissiez vérifier que tout ce que je dis est vrai ; ensuite pour que, si je m’arrête, ce projet ne disparaisse pas avec moi. La licence est MIT.',
    q3: 'Les modèles du groupe gratuit deviendront-ils payants ?',
    a3: 'Le groupe gratuit n’est tout simplement pas facturé — je ne joue pas au jeu du « gratuit d’abord, payant une fois accroché ». Le périmètre gratuit peut évoluer avec les prix en amont, mais j’annoncerai tout changement à l’avance.',
    q4: 'Mes données d’appel seront-elles utilisées ?',
    a4: 'Non. Les journaux ne servent qu’à la facturation et au dépannage, et sont stockés sur mon propre serveur. C’est indiqué dans la politique de confidentialité du site.',
    q5: 'Que faire si l’amont est instable ?',
    a5: 'Pools de clés + nouvelles tentatives automatiques + temporisation : une clé échoue, la suivante prend le relais ; tout un canal échoue, on change de canal et on réessaie. Vous n’envoyez toujours qu’une seule requête.',
    q6: 'Comment vous contacter ?',
    a6: 'Vous me trouverez via « Nous contacter » et « Signaler un abus » en bas de page. Dites simplement ce qu’il en est.',
  },
  cta: {
    titleLoggedIn: 'Commencez par un nouveau jeton.',
    titleGuest: 'Commencez par un jeton.',
    descLoggedIn: 'Branchez-le sur votre code existant, faites passer une requête, puis décidez si vous restez.',
    descGuest: 'L’inscription est gratuite — faites passer une requête, puis décidez si vous restez.',
  },
}
