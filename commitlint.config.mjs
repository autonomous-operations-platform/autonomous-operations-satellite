
/** @type {import('@commitlint/types').UserConfig} */
const config = {
  extends: ['@commitlint/config-conventional'],
  rules: {
    'type-enum': [
      2,
      'always',
      ['feat', 'fix', 'chore', 'docs', 'refactor', 'test', 'perf', 'ci', 'build', 'revert'],
    ],
    'scope-enum': [
      2,
      'always',
      ['deps', 'devops', 'security', 'deployment', 'architecture', 'pipeline', 'github', 'docs', 'code'],
    ],
    'scope-case': [2, 'always', 'kebab-case'],
    'subject-case': [2, 'never', ['start-case', 'pascal-case', 'upper-case']],
    'subject-full-stop': [2, 'never', '.'],
    'header-max-length': [2, 'always', 120],
  },

  parserPreset: {
    parserOpts: {
      headerPattern: /^(\w*)(?:\(([\w\s-]*)\))?!?:\s(.*)$/,
    },
  },
}

export default config
