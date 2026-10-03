/**
 * 法律 / 信息页词条（English）：用户协议、隐私政策、安全致谢、联系方式、
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
    title: 'Terms of Service',
    updatedAt: 'Last updated: 2026-09-30',
    s1Title: '1. General provisions',
    s1Body:
      'This service is an LLM API relay service provided by the operator of this site. By using it you confirm that you have read and agree to all terms of this agreement; if you do not agree, please stop using the service.',
    s2Title: '2. Account and security',
    s2Body:
      'You must keep your account and access tokens safe, and must not lend, transfer or misuse them. You are responsible for the consequences of poor safekeeping. If you find your account has been compromised, contact an admin immediately.',
    s3Title: '3. Balance and billing',
    s3Body:
      'Balances are denominated in RMB; top-ups are used to offset API call fees and constitute no virtual currency or transferable right. Balances are for your personal use only and cannot be transferred or withdrawn. Billing rules follow the published model plaza, and any price change will be announced on the site beforehand.',
    s4Title: '4. Statement on upstream capabilities based on subscription accounts',
    s4Body1:
      'This site may connect to upstream capabilities credentialed by third-party subscription accounts, in order to expand the models and routes available. Such capabilities are offered only for learning, research and technical validation; they supplement existing services and constitute no commercial commitment or resale authorization of any kind.',
    s4Body2:
      'When using such capabilities, you must ensure on your own that you comply with the terms of service of the relevant upstream providers and the laws of your jurisdiction; you must not use them for commercial resale, bulk scraping, circumventing upstream restrictions or quota controls, or any purpose beyond the authorized scope.',
    s4Body3:
      'All consequences arising from your breach of the above or of the upstream providers’ terms (including but not limited to restrictions on upstream accounts, service interruption, disputes or legal liability) are borne by you, and this site bears no joint liability. This site only provides technical access and makes no guarantee as to the availability or compliance of upstream accounts.',
    s4Body4:
      'This site reserves the right to suspend or terminate the relevant services and to restrict or deactivate the corresponding accounts upon receiving complaints from upstream providers, regulatory requirements, or discovering misuse.',
    s5Title: '5. Content compliance',
    s5Body:
      'You must not generate or disseminate illegal or non-compliant content through this service (including but not limited to endangering national security, pornography or violence, or infringing others’ rights). This site has sensitive-word filtering and content-safety mechanisms; violating content will be rejected and may trigger account actions.',
    s6Title: '6. Service availability',
    s6Body:
      'This service strives for stable operation but makes no commitment to absolute availability. For unavailability caused by upstream provider failures, network fluctuations or planned maintenance, this site will do its best to restore service but bears no indirect losses arising therefrom.',
    s7Title: '7. Disclaimer',
    s7Body:
      'This site bears no responsibility for losses caused by force majeure, hacking, system failure, third-party service interruptions or other causes not attributable to this site. See the full Disclaimer for a more complete statement.',
    s8Title: '8. Changes to the agreement',
    s8Body:
      'This site may revise this agreement as needed for operations, and any revision will be published on the site. Continued use constitutes acceptance of the revised terms.',
  },
  privacy: {
    title: 'Privacy Policy',
    updatedAt: 'Last updated: 2026-09-30',
    s1Title: '1. What information we collect',
    s1Body:
      'Account information collected at registration (username, email, password hash); the request content and usage information in call logs; and necessary device and network information (for security and risk control).',
    s2Title: '2. How information is used',
    s2Body:
      'To provide and maintain the service, handle billing, troubleshoot, ensure security and perform compliance auditing. We do not sell or rent your personal information to any third party.',
    s3Title: '3. How information is stored',
    s3Body:
      'Data is stored on this site’s own servers (SQLite local storage by default). Passwords are stored as salted hashes, never in plaintext. Plaintext access tokens are shown only once at creation.',
    s4Title: '4. Your rights',
    s4Body:
      'You have the right to access, correct or delete your account information and related data. To delete your account, please contact an admin. Content compliance: for request content that violates the law, we will retain it as required by law and cooperate with regulators.',
    s5Title: '5. Protection of minors',
    s5Body:
      'This service is not offered to minors under 18. If you are a guardian and find that a minor is using this service, please contact us.',
    s6Title: '6. Changes to this policy',
    s6Body:
      'If this policy changes materially, we will publish it prominently on the site. Continued use constitutes acceptance of the updated policy.',
  },
  security: {
    title: 'Security thanks',
    updatedAt: 'Last updated: 2026-09-30',
    s1Title: 'Reporting a vulnerability',
    s1Body:
      'If you find a security vulnerability on this site, we welcome responsible disclosure: do not spread the details publicly; contact an admin first by email or group chat, and we will publicly thank you after fixing it.',
    s2Title: 'What to include',
    s2Body:
      'Please describe: the vulnerability type, its impact, reproduction steps (as concise as possible) and a suggested fix. Please do not perform destructive testing.',
    s3Title: 'Credits',
    s3Body: 'We thank the following researchers for their contributions to this site’s security:',
    thanksRevealed: '(The list will be published here once valid reports are received)',
    thanksShow: 'View credited researchers',
  },
  contact: {
    title: 'Contact',
    updatedAt: 'Last updated: 2026-09-30',
    emailTitle: 'Support email',
    emailBody: 'For business enquiries, partnerships or help, email us at:',
    emailNotSet: '(No public email configured yet; please reach us via the community group)',
    groupTitle: 'Community group',
    groupBodyPrefix: 'If you run into problems, join the community group for help: ',
    groupLink: 'Join the community',
    groupBodySuffix: '.',
    hoursTitle: 'Response hours',
    hoursBody:
      'We aim to reply to emails within 48 hours. For urgent issues (such as a compromised account), please also notify an admin via the community group.',
  },
  join: {
    title: 'Join the community',
    subtitle: 'Have a question, a feature request, or just want to talk about model tech — come join us.',
    groupMainName: 'LTZY-API main group',
    groupMainDesc: 'Discuss usage questions, channel onboarding and new feature previews.',
    joinButton: 'Join',
  },
  report: {
    title: 'Report abuse',
    updatedAt: 'Last updated: 2026-09-30',
    s1Title: 'Scope',
    s1Body:
      '1) Complaints about the quality of this site’s service; 2) content published by this site or its users that is suspected of being illegal or non-compliant; 3) security issues such as compromised or abused accounts.',
    s2Title: 'How to report',
    s2BodyPrefix: 'Please submit reports by email:',
    emailNotSet: '(No public report email configured yet)',
    s2BodySuffix:
      '. Please attach relevant evidence where possible (screenshots, times, the content involved) so that we can verify quickly.',
    s3Title: 'Response time',
    s3Body:
      'We undertake to respond to reports within 48 hours and, after verification, to take necessary measures as required by law (removing content, handling accounts, reporting to regulators, etc.).',
    s4Title: 'Liability for false reports',
    s4Body: 'We reserve the right to pursue legal liability for deliberately fabricated or malicious reports.',
  },
  install: {
    checking: 'Checking installation status…',
    doneTitle: 'Installation complete',
    doneDesc: 'An administrator account has been configured. Please sign in to the admin panel.',
    doneButton: 'Go to admin sign-in',
    wizardBrand: 'Setup wizard',
    title: 'Create an administrator',
    subtitle: 'This is the first installation; please set up the site administrator account',
    siteNameLabel: 'Site name',
    siteNameHelp: 'Shown in the page title and footer',
    siteNamePlaceholder: 'e.g. LTZY-API Gateway',
    usernameLabel: 'Admin username',
    passwordLabel: 'Admin password',
    passwordHelp: 'At least 8 characters',
    passwordPlaceholder: 'At least 8 characters',
    confirmLabel: 'Confirm password',
    confirmPlaceholder: 'Re-enter',
    submit: 'Finish installation',
    haveAccount: 'Already have an account?',
    loginLink: 'Sign in',
    errPasswordShort: 'Password must be at least 8 characters',
    errPasswordMismatch: 'The two passwords do not match',
    errFailed: 'Installation failed',
  },
}
